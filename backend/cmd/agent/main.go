package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cloudprint/internal/agentclient"
)

func main() {
	server := flag.String("server", "", "CloudPrint base URL, e.g. http://nas:8080")
	token := flag.String("token", "", "agent bearer token")
	endpoint := flag.String("endpoint", "", "agent endpoint id")
	command := flag.String("print-command", "", "optional print command; %s is replaced with PDF path")
	interval := flag.Duration("interval", 4*time.Second, "claim poll interval")
	once := flag.Bool("once", false, "send one heartbeat, then exit")
	flag.Parse()
	if *server == "" || *token == "" || *endpoint == "" {
		fmt.Fprintln(os.Stderr, "usage: cloudprint-agent -server URL -token TOKEN -endpoint ENDPOINT_ID [-print-command CMD]")
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client := agentclient.New(*server, *token, *endpoint, "0.1.0")
	if *command == "" {
		*command = os.Getenv("CLOUDPRINT_PRINT_COMMAND")
	}
	status := map[string]any{
		"hostname":                 hostname(),
		"print_command_configured": *command != "",
		"endpoint_id":              *endpoint,
	}
	ctx := context.Background()
	if err := client.Heartbeat(ctx, []any{}, status); err != nil {
		log.Error("heartbeat failed", "error", err)
		os.Exit(1)
	}
	log.Info("agent connected", "server", *server, "endpoint", *endpoint)
	if *once {
		return
	}
	// Keep the endpoint online even while a long print command is running.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				if err := client.Heartbeat(hbCtx, []any{}, status); err != nil {
					log.Warn("heartbeat failed", "error", err)
				}
			}
		}
	}()
	for {
		claim, err := client.Claim(ctx, 900)
		if err != nil {
			log.Warn("claim failed", "error", err)
			time.Sleep(10 * time.Second)
			continue
		}
		if claim == nil || claim.Job == nil {
			time.Sleep(*interval)
			continue
		}
		commandToRun := *command
		if commandToRun == "" {
			commandToRun = claim.PrintCommand
		}
		runJob(ctx, client, claim, commandToRun, log)
	}
}

func runJob(ctx context.Context, c *agentclient.Client, claim *agentclient.ClaimResult, command string, log *slog.Logger) {
	job := claim.Job
	log.Info("job claimed", "job", job.ID, "file", job.FileName, "pages", job.PageCount)
	tmp, err := os.MkdirTemp("", "cloudprint-agent-*")
	if err != nil {
		_ = c.Complete(ctx, job.ID, agentclient.Result{Success: false, ErrorCode: "TMP_FAILED", ErrorMessage: err.Error()})
		return
	}
	defer os.RemoveAll(tmp)
	pdf := filepath.Join(tmp, "print.pdf")
	if err := c.Download(ctx, job.PDFFile, pdf); err != nil {
		_ = c.Complete(ctx, job.ID, agentclient.Result{Success: false, ErrorCode: "DOWNLOAD_FAILED", ErrorMessage: err.Error()})
		return
	}
	if err := c.Start(ctx, job.ID); err != nil {
		log.Warn("start report failed", "error", err)
	}
	if command == "" {
		msg := "未配置打印命令；请在代理参数 -print-command 或环境变量 CLOUDPRINT_PRINT_COMMAND 中指定 PDF 打印命令"
		log.Error(msg)
		_ = c.Complete(ctx, job.ID, agentclient.Result{Success: false, ErrorCode: "PRINT_COMMAND_MISSING", ErrorMessage: msg})
		return
	}
	cmdline := expandFilePlaceholder(command, pdf)
	pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(pctx, "cmd.exe", "/c", cmdline)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = c.Complete(ctx, job.ID, agentclient.Result{
			Success: false, ErrorCode: "PRINT_FAILED",
			ErrorMessage: strings.TrimSpace(string(out)) + " " + err.Error(),
		})
		log.Error("print failed", "job", job.ID, "error", err, "output", string(out))
		return
	}
	_ = c.Complete(ctx, job.ID, agentclient.Result{Success: true})
	log.Info("job completed", "job", job.ID)
}

// expandFilePlaceholder substitutes %s / %FILE%. A placeholder that is already
// wrapped in quotes keeps that quoting (otherwise "%s" would become ""path""),
// while a bare placeholder is quoted because temp paths usually contain spaces.
func expandFilePlaceholder(command, path string) string {
	tokens := []string{"%s", "%FILE%"}
	var b strings.Builder
	for i := 0; i < len(command); {
		matched := ""
		for _, token := range tokens {
			if strings.HasPrefix(command[i:], token) {
				matched = token
				break
			}
		}
		if matched == "" {
			b.WriteByte(command[i])
			i++
			continue
		}
		end := i + len(matched)
		var before, after byte
		if i > 0 {
			before = command[i-1]
		}
		if end < len(command) {
			after = command[end]
		}
		if (before == '"' && after == '"') || (before == '\'' && after == '\'') {
			b.WriteString(path)
		} else {
			b.WriteString(`"`)
			b.WriteString(path)
			b.WriteString(`"`)
		}
		i = end
	}
	return b.String()
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "windows"
	}
	return h
}
