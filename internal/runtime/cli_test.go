package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// TestHelperProcess is not a real test: it is the fake agent CLI that the
// tests below execute. It reports the arguments after "--" and its stdin.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("SIGNALBOX_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	stdin, _ := io.ReadAll(os.Stdin)
	last := ""
	if len(args) > 0 {
		last = args[len(args)-1]
	}
	fmt.Printf("args=%d last=%d stdin=%d", len(args), len(last), len(stdin))
	os.Exit(0)
}

func helperRuntime(t *testing.T, rc config.RuntimeConfig) Runtime {
	t.Helper()
	t.Setenv("SIGNALBOX_HELPER", "1")
	rc.Command = os.Args[0]
	rc.Args = []string{"-test.run=^TestHelperProcess$", "--", "exec"}
	rt, err := newCLI("agent", rc)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestCodeReceivesInputOnStdin(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "verify.py")
	if err := os.WriteFile(source, []byte("print('verified')"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := NewCode(&pipeline.Node{ID: "verify", Language: "python", CodeSource: "verify.py", TimeoutSeconds: 5}, root)
	if err != nil {
		t.Fatal(err)
	}
	code := rt.(*cliRuntime)
	if code.dir != root || code.promptMode != "stdin" {
		t.Fatalf("code dir/mode = %q/%q", code.dir, code.promptMode)
	}
	t.Setenv("SIGNALBOX_HELPER", "1")
	code.command = os.Args[0]
	code.args = []string{"-test.run=^TestHelperProcess$", "--", source}
	input := strings.Repeat("{\"data\":\"Unicode and large input\"}\n", 2000)
	output, err := code.Invoke(context.Background(), Request{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("args=1 last=%d stdin=%d", len(source), len(input))
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	if _, err := NewCode(&pipeline.Node{ID: "missing", Language: "python", CodeSource: "missing.py"}, root); err == nil {
		t.Fatal("missing code-source was accepted")
	}
}

func TestCLIShortPromptIsPassedAsArgument(t *testing.T) {
	rt := helperRuntime(t, config.RuntimeConfig{Type: "codex"})
	out, err := rt.Invoke(context.Background(), Request{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "args=2 last=5 stdin=0" {
		t.Fatalf("output = %q", out)
	}
}

func TestCLILongPromptFallsBackToStdin(t *testing.T) {
	rt := helperRuntime(t, config.RuntimeConfig{Type: "codex"})
	input := strings.Repeat(`line with "quotes" and C:\paths`+"\n", 4000)
	out, err := rt.Invoke(context.Background(), Request{SystemPrompt: "judge", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("args=2 last=1 stdin=%d", len("judge\n\n---\n\n")+len(input))
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestCLILongPromptWithoutStdinSupportFailsClearly(t *testing.T) {
	rt := helperRuntime(t, config.RuntimeConfig{Type: "cli"})
	_, err := rt.Invoke(context.Background(), Request{Input: strings.Repeat("x", maxArgPrompt+1)})
	if err == nil || !strings.Contains(err.Error(), "prompt_mode: stdin") {
		t.Fatalf("err = %v", err)
	}
}

func TestCLIStdinModeAlwaysUsesStdin(t *testing.T) {
	rt := helperRuntime(t, config.RuntimeConfig{Type: "cli", PromptMode: "stdin"})
	out, err := rt.Invoke(context.Background(), Request{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "args=1 last=4 stdin=5" {
		t.Fatalf("output = %q", out)
	}
}

// TestFilesHelperProcess is a fake agent CLI that echoes its arguments, the
// SIGNALBOX_FILES_DIR variable and stdin.
func TestFilesHelperProcess(t *testing.T) {
	if os.Getenv("SIGNALBOX_FILES_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	stdin, _ := io.ReadAll(os.Stdin)
	fmt.Printf("args=%s\nenv=%s\nstdin=%s", strings.Join(args, "|"), os.Getenv(FilesDirEnv), stdin)
	os.Exit(0)
}

func TestCLIReceivesUploadedFiles(t *testing.T) {
	dir := t.TempDir()
	req := Request{Input: "go", FilesDir: dir, Files: []string{filepath.Join(dir, "a.csv")}}

	t.Setenv("SIGNALBOX_FILES_HELPER", "1")
	rt, err := newCLI("agent", config.RuntimeConfig{Type: "codex", Command: os.Args[0], Args: []string{"-test.run=^TestFilesHelperProcess$", "--"}, PromptMode: "stdin"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := rt.Invoke(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "env="+dir+"\n") || !strings.Contains(out, "Attached files (read-only copies in "+dir+"):\n- "+filepath.Join(dir, "a.csv")) || strings.Contains(out, "--add-dir") {
		t.Fatalf("codex output = %q", out)
	}
	out, err = rt.Invoke(context.Background(), Request{Input: "go"})
	if err != nil || strings.Contains(out, "Attached files") || !strings.Contains(out, "env=\n") {
		t.Fatalf("no-files output = %q %v", out, err)
	}

	for kind, want := range map[string]string{"claude": "--add-dir " + dir, "copilot": "--add-dir " + dir, "codex": "", "cli": ""} {
		c := &cliRuntime{kind: kind}
		args, env := c.filesArgs(req)
		if strings.Join(args, " ") != want || len(env) != 1 || env[0] != FilesDirEnv+"="+dir {
			t.Fatalf("%s filesArgs = %v %v", kind, args, env)
		}
	}
	code := &cliRuntime{kind: "claude", code: true}
	if args, env := code.filesArgs(req); len(args) != 0 || len(env) != 1 {
		t.Fatalf("code filesArgs = %v %v", args, env)
	}
	if args, env := (&cliRuntime{kind: "claude"}).filesArgs(Request{}); args != nil || env != nil {
		t.Fatalf("no files = %v %v", args, env)
	}
}
