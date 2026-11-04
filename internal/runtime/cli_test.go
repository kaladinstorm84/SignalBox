package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gitea/signalbox/internal/config"
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
