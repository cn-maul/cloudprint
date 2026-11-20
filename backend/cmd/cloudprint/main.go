package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"cloudprint/internal/config"
	"cloudprint/internal/database"
	"cloudprint/internal/files"
	"cloudprint/internal/store"
	"cloudprint/internal/webapi"
)

//go:embed all:ui
var uiFS embed.FS

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	for _, dir := range []string{
		cfg.DataDir,
		filepath.Join(cfg.DataDir, "originals"),
		filepath.Join(cfg.DataDir, "pdf"),
		filepath.Join(cfg.DataDir, "previews"),
		filepath.Join(cfg.DataDir, "tmp"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create data dir %s: %w", dir, err)
		}
	}
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	st := store.New(db)
	generatedPW, err := database.Seed(context.Background(), st, cfg)
	if err != nil {
		return err
	}
	// Older installs only stored a Windows-helper endpoint (which has no IP).
	// Recover the LAN address so discovery does not offer the same printer again.
	_ = st.BackfillPrinterAddresses(context.Background())
	if generatedPW != "" {
		log.Warn("initial admin account created",
			"username", cfg.AdminUsername, "password", generatedPW,
			"note", "只显示这一次，请登录后立即修改")
		fmt.Printf("\n========================================\n")
		fmt.Printf("初始管理员: %s\n", cfg.AdminUsername)
		fmt.Printf("初始密码  : %s\n", generatedPW)
		fmt.Printf("登录后请立即修改密码。\n")
		fmt.Printf("========================================\n\n")
	}
	pipeline := files.NewPipeline(cfg.DataDir, cfg.PDFToPPM)
	ui, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return err
	}
	srv := webapi.New(cfg, st, pipeline, log, ui)
	handler := srv.Routes()

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	go (&webapi.Worker{Server: srv, Log: log}).Run(workerCtx)
	go cleanupLoop(workerCtx, st, pipeline, log)
	go probeLoop(workerCtx, srv, log)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("cloudprint listening", "addr", cfg.Addr, "data", cfg.DataDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String())
	}
	workerCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpSrv.Shutdown(ctx)
}

func cleanupLoop(ctx context.Context, st *store.Store, pipeline *files.Pipeline, log *slog.Logger) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			files, err := st.ExpiredFiles(ctx, 100)
			if err != nil {
				log.Warn("list expired files failed", "error", err)
				continue
			}
			for _, f := range files {
				retained, active, err := st.PathUsage(ctx, f.StoragePath)
				if err != nil {
					continue
				}
				if retained > 0 {
					// Another row still within its retention window shares the
					// content-addressed bytes; only drop this stale pointer.
					_ = st.DetachExpiredFile(ctx, f.ID)
					continue
				}
				if active > 0 {
					continue
				}
				_ = os.Remove(f.StoragePath)
				_ = st.PurgePath(ctx, f.StoragePath)
				_ = os.Remove(filepath.Join(pipeline.Root, "previews", f.ID+".png"))
			}
		}
	}
}

func probeLoop(ctx context.Context, srv *webapi.Server, log *slog.Logger) {
	time.Sleep(20 * time.Second)
	srv.Printer.ProbeAll(ctx)
}
