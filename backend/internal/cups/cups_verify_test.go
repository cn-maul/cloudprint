package cups

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTools builds lp/lpstat/lpadmin stand-ins so the spooler path can be
// exercised on a machine without CUPS.
func fakeTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	prog := `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	args := os.Args[1:]
	has := func(s string) bool {
		for _, a := range args {
			if a == s {
				return true
			}
		}
		return false
	}
	switch name {
	case "lpstat":
		if has("-r") {
			fmt.Println("scheduler is running")
			return
		}
		if has("-p") && !has("-l") {
			fmt.Println("printer pantum-ms6000nw is idle.  enabled since Thu 19 Nov 2026")
			return
		}
		if has("-l") {
			fmt.Println("printer pantum-ms6000nw is idle.  enabled since Thu 19 Nov 2026")
			fmt.Println("\tDevice URI: ipp://192.168.2.91/ipp/print")
			fmt.Println("\tMake and Model: Pantum MS6000NW Series")
			return
		}
		if has("-W") {
			for _, a := range args {
				if a == "completed" {
					fmt.Println("pantum-42  user  1024  Thu 19 Nov 2026")
				}
			}
			return
		}
		return
	case "lpadmin":
		if has("-x") {
			return
		}
		uri := ""
		for i, a := range args {
			if a == "-v" && i+1 < len(args) {
				uri = args[i+1]
			}
		}
		fmt.Fprintf(os.Stderr, "created %s -> %s\n", args[1], uri)
		return
	case "lp":
		fmt.Println("request id is pantum-42 (1 file(s))")
		return
	}
	os.Exit(2)
}
`
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lp", "lpstat", "lpadmin"} {
		out := filepath.Join(dir, name+".exe")
		cmd := exec.Command("go", "build", "-o", out, src)
		cmd.Env = os.Environ()
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, b)
		}
	}
	return dir
}

func TestSpoolerPath(t *testing.T) {
	dir := fakeTools(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ppd := filepath.Join(dir, "Pantum MS6000NW Series.ppd")
	if err := os.WriteFile(ppd, []byte("*NickName: \"Pantum MS6000NW Series\"\n*Manufacturer: \"Pantum\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUDPRINT_PPD_DIRS", dir)

	c := New("")
	if ok, reason := c.Available(); !ok {
		t.Fatalf("Available() = false: %s", reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queues, err := c.Queues(ctx)
	if err != nil {
		t.Fatalf("Queues: %v", err)
	}
	if len(queues) != 1 || queues[0].Name != "pantum-ms6000nw" {
		t.Fatalf("unexpected queues: %+v", queues)
	}
	if queues[0].MakeModel != "Pantum MS6000NW Series" || queues[0].DeviceURI == "" {
		t.Fatalf("details not parsed: %+v", queues[0])
	}

	if err := c.Create(ctx, CreateOptions{
		Name: "pantum-ms6000nw", DeviceURI: "ipp://192.168.2.91/ipp/print", PPD: ppd,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	id, err := c.Submit(ctx, "pantum-ms6000nw", ppd, SubmitOptions{Copies: 2, Duplex: "two-sided-long-edge"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id != "pantum-42" {
		t.Fatalf("job id = %q", id)
	}
	state, err := c.JobState(ctx, "pantum-ms6000nw", id)
	if err != nil || state != "completed" {
		t.Fatalf("JobState = %q, %v", state, err)
	}

	if best := c.MatchPPD("MS6000NW series"); best == nil || !strings.Contains(best.Name, "MS6000NW") {
		t.Fatalf("MatchPPD failed: %+v", best)
	}
	if got := QueueName("三楼 打印机", "pr_1234abcd"); got != "cloudprint-1234abcd" {
		t.Fatalf("QueueName = %q", got)
	}
}

// TestMatchPPDVariant checks the driver picker against a name shape the real
// vendor pack uses: "M6500NW" and "M6500NW Plus" are separate PPDs, and picking
// the wrong one sends jobs to a driver for a different machine.
func TestMatchPPDVariant(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "Pantum")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, nick := range map[string]string{
		"Pantum M6500NW Plus series.ppd": "Pantum M6500NW Plus-series",
		"Pantum M6500NW Series.ppd":      "Pantum M6500NW Series",
		"Pantum MS6000NW Series.ppd":     "Pantum MS6000NW Series",
	} {
		body := "*Manufacturer: \"Pantum\"\n*NickName: \"" + nick + "\"\n*ModelName: \"" + nick + "\"\n"
		if err := os.WriteFile(filepath.Join(base, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLOUDPRINT_PPD_DIRS", base)
	c := New("")
	cases := map[string]string{
		"MS6000NW series":   "Pantum MS6000NW Series",
		"M6500NW":           "Pantum M6500NW Series",
		"Pantum M6500NW":    "Pantum M6500NW Series",
		"Totally Unknown Z": "",
	}
	for model, want := range cases {
		got := ""
		if p := c.MatchPPD(model); p != nil {
			got = p.Name
		}
		if got != want {
			t.Errorf("MatchPPD(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestQueueNameIsUniquePerDevice(t *testing.T) {
	dir := t.TempDir()
	// A queue named "ms6000nw" already belongs to another printer at .50.
	prog := `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	name := strings.TrimSuffix(strings.ToLower(os.Args[0]), ".exe")
	args := strings.Join(os.Args[1:], " ")
	switch {
	case strings.Contains(name, "lpstat"):
		switch {
		case strings.Contains(args, "-r"):
			fmt.Println("scheduler is running")
		case strings.Contains(args, "-l"):
			fmt.Println("printer ms6000nw is idle.  enabled since Thu 19 Nov 2026")
			fmt.Println("\tDevice URI: ipp://192.168.2.50/ipp/print")
			fmt.Println("\tMake and Model: Pantum MS6000NW Series")
		case strings.Contains(args, "-p"):
			fmt.Println("printer ms6000nw is idle.  enabled since Thu 19 Nov 2026")
		}
	case strings.Contains(name, "lpadmin"):
		return
	default:
		fmt.Println("request id is x-1 (1 file(s))")
	}
}
`
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lpstat", "lpadmin", "lp"} {
		out := filepath.Join(dir, name+".exe")
		cmd := exec.Command("go", "build", "-o", out, src)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, b)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	c := New("")
	c.Lpstat = filepath.Join(dir, "lpstat")
	c.Lp = filepath.Join(dir, "lp")
	c.Lpadmin = filepath.Join(dir, "lpadmin")
	got, err := c.UniqueQueueName(context.Background(), "MS6000NW", "pr_abcdef12", "ipp://192.168.2.91/ipp/print")
	if err != nil {
		t.Fatalf("UniqueQueueName: %v", err)
	}
	if got == "ms6000nw" {
		t.Fatalf("reused a queue that belongs to another device: %q", got)
	}
	if !strings.HasPrefix(got, "ms6000nw-") {
		t.Fatalf("unexpected queue name %q", got)
	}
}
