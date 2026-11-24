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
	"strings"
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
	// A NAS is not a server that runs for months: every redeploy restarts the
	// process, and a ticker that only fires after six hours would never clean
	// anything on a box updated more often than that. Sweep once at startup,
	// then every six hours.
	cleanupOnce(ctx, st, pipeline, log)
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cleanupOnce(ctx, st, pipeline, log)
		}
	}
}

func cleanupOnce(ctx context.Context, st *store.Store, pipeline *files.Pipeline, log *slog.Logger) {
	sweepExpired(ctx, st, pipeline, log)
	sweepOrphanPreviews(ctx, st, pipeline, log)
	sweepOrphanUploads(ctx, st, pipeline, log)
}

// sweepExpired removes every expired upload, in batches rather than one page
// of 100: a box that has been running for weeks can accumulate more than that
// between sweeps, and stopping after one batch left the rest until the next
// six-hour tick.
func sweepExpired(ctx context.Context, st *store.Store, pipeline *files.Pipeline, log *slog.Logger) {
	// Rows that cannot be removed yet (a job still printing them) stay in the
	// table, so without remembering them the same first 100 would be fetched
	// forever and everything after them would never be reached.
	skip := map[string]bool{}
	for {
		expired, err := st.ExpiredFiles(ctx, 100)
		if err != nil {
			log.Warn("list expired files failed", "error", err)
			return
		}
		batch := expired[:0]
		for _, f := range expired {
			if !skip[f.ID] {
				batch = append(batch, f)
			}
		}
		if len(batch) == 0 {
			return
		}
		for _, f := range batch {
			retained, active, err := st.PathUsage(ctx, f.StoragePath)
			if err != nil {
				skip[f.ID] = true
				continue
			}
			if retained > 0 {
				// Another row still within its retention window shares the
				// content-addressed bytes; only drop this stale pointer.
				_ = st.DetachExpiredFile(ctx, f.ID)
				removePreviews(ctx, pipeline.Root, f.ID)
				continue
			}
			if active > 0 {
				skip[f.ID] = true
				continue
			}
			_ = os.Remove(f.StoragePath)
			_ = st.PurgePath(ctx, f.StoragePath)
			removePreviews(ctx, pipeline.Root, f.ID)
			log.Info("expired upload removed", "file", f.ID, "name", f.OriginalName)
		}
		if len(expired) < 100 {
			return
		}
	}
}

// removePreviews deletes every cached page image for one file. Preview files
// are named "<id>-p<page>.png"; the old code only removed "<id>.png", which
// never existed, so previews of deleted uploads stayed on the NAS forever.
func removePreviews(ctx context.Context, root, fileID string) {
	if err := ctx.Err(); err != nil {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(root, "previews", fileID+"-p*.png"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// sweepOrphanPreviews drops preview images left by uploads that have already
// been cleaned. Older builds deleted "<id>.png" while the renderer writes
// "<id>-p<page>.png", so every deleted upload left its pages behind.
func sweepOrphanPreviews(ctx context.Context, st *store.Store, pipeline *files.Pipeline, log *slog.Logger) {
	retained, err := st.RetainedFileIDs(ctx)
	if err != nil {
		log.Warn("list retained files failed", "error", err)
		return
	}
	dir := filepath.Join(pipeline.Root, "previews")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("read previews failed", "error", err)
		}
		return
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, ok := previewFileID(e.Name())
		if !ok || retained[id] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			removed++
		}
	}
	if removed > 0 {
		log.Info("orphan previews removed", "count", removed)
	}
}

// previewFileID extracts the owning file id from "<id>-p<page>.png".
func previewFileID(name string) (string, bool) {
	if !strings.HasSuffix(name, ".png") {
		return "", false
	}
	i := strings.LastIndex(name, "-p")
	if i <= 0 {
		return "", false
	}
	id := name[:i]
	for _, r := range name[i+2 : len(name)-len(".png")] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return id, true
}

// sweepOrphanUploads deletes originals and rendered PDFs that no row points at
// anymore. Deleting a user cascades its files rows away but not the bytes, so
// without this every removed account left its uploads on the NAS forever.
//
// Only files older than a day are touched: an upload is written to disk before
// its row is inserted, and a sweep landing in that gap would delete a healthy
// upload mid-request.
func sweepOrphanUploads(ctx context.Context, st *store.Store, pipeline *files.Pipeline, log *slog.Logger) {
	retained, err := st.RetainedStoragePaths(ctx)
	if err != nil {
		log.Warn("list retained paths failed", "error", err)
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	removed := 0
	for _, dir := range []string{"originals", "pdf"} {
		root := filepath.Join(pipeline.Root, dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil || info.ModTime().After(cutoff) {
				return nil
			}
			if retained[path] {
				return nil
			}
			if err := os.Remove(path); err == nil {
				removed++
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			log.Warn("scan uploads failed", "dir", dir, "error", err)
		}
	}
	if removed > 0 {
		log.Info("orphan uploads removed", "count", removed)
	}
}

func probeLoop(ctx context.Context, srv *webapi.Server, log *slog.Logger) {
	time.Sleep(20 * time.Second)
	srv.Printer.ProbeAll(ctx)
}
