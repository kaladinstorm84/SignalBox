package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

func TestOpenSpecHelperProcess(t *testing.T) {
	if os.Getenv("SIGNALBOX_OPENSPEC_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	switch args[0] {
	case "fail":
		fmt.Fprint(os.Stdout, `{"valid":false}`)
		fmt.Fprint(os.Stderr, "OpenSpec validation failed")
		os.Exit(2)
	case "sleep":
		time.Sleep(10 * time.Second)
	}
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Args  []string
		Dir   string
		Input string
	}{args, dir, string(input)}); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func openSpecHelper(t *testing.T, args ...string) *cliRuntime {
	t.Helper()
	t.Setenv("SIGNALBOX_OPENSPEC_HELPER", "1")
	rt, err := NewOpenSpec(&pipeline.Node{
		ID: "spec", TimeoutSeconds: 5,
		OpenSpec: &pipeline.OpenSpecSpec{
			Command: os.Args[0],
			Args:    append([]string{"-test.run=^TestOpenSpecHelperProcess$", "--"}, args...),
		},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return rt.(*cliRuntime)
}

func TestOpenSpecPassesOnlyExplicitArgumentsInProjectRoot(t *testing.T) {
	args := []string{"instructions", "proposal", "--change", "a value with spaces & shell characters", "--json"}
	rt := openSpecHelper(t, args...)
	if rt.Name() != "spec" {
		t.Fatalf("name = %q", rt.Name())
	}
	output, err := rt.Invoke(context.Background(), Request{
		SystemPrompt: "must not be passed", Input: strings.Repeat("handoff text ", 3000),
	})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Args  []string
		Dir   string
		Input string
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Args, args) || result.Dir != rt.dir || result.Input != "" {
		t.Fatalf("command received %+v", result)
	}
}

func TestOpenSpecFailuresAndCancellation(t *testing.T) {
	t.Run("nonzero exit", func(t *testing.T) {
		rt := openSpecHelper(t, "fail")
		_, err := rt.Invoke(context.Background(), Request{})
		var failure *ProcessExitError
		if !errors.As(err, &failure) || failure.ExitCode != 2 || failure.Stdout != `{"valid":false}` ||
			!strings.Contains(failure.Stderr, "validation failed") {
			t.Fatalf("error = %#v", err)
		}
	})
	t.Run("missing executable", func(t *testing.T) {
		rt := openSpecHelper(t, "list")
		rt.command = filepath.Join(t.TempDir(), "missing-openspec")
		if _, err := rt.Invoke(context.Background(), Request{}); err == nil {
			t.Fatal("missing executable reported success")
		}
	})
	t.Run("deadline", func(t *testing.T) {
		rt := openSpecHelper(t, "sleep")
		rt.timeout = 100 * time.Millisecond
		start := time.Now()
		_, err := rt.Invoke(context.Background(), Request{})
		if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 3*time.Second {
			t.Fatalf("deadline error = %v, elapsed = %s", err, time.Since(start))
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		rt := openSpecHelper(t, "sleep")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := rt.Invoke(ctx, Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	})
}

func TestOpenSpecConstructor(t *testing.T) {
	node := &pipeline.Node{ID: "spec", OpenSpec: &pipeline.OpenSpecSpec{Args: []string{"list", "--json"}}}
	root := t.TempDir()
	rt, err := NewOpenSpec(node, root)
	if err != nil {
		t.Fatal(err)
	}
	cli := rt.(*cliRuntime)
	if cli.command != "openspec" || cli.timeout != 10*time.Minute || cli.dir != root || !cli.commandOnly {
		t.Fatalf("defaults = %+v", cli)
	}
	node.OpenSpec.Args[0] = "changed"
	if cli.args[0] != "list" {
		t.Fatal("constructor retained mutable args")
	}
	for _, root := range []string{"", filepath.Join(root, "missing")} {
		if _, err := NewOpenSpec(node, root); err == nil {
			t.Fatalf("invalid root %q accepted", root)
		}
	}
	if _, err := NewOpenSpec(&pipeline.Node{ID: "missing"}, root); err == nil {
		t.Fatal("missing config was accepted")
	}
}

func TestOpenSpecWindowsNPMShimsDoNotUseShellParsing(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("Windows npm shim handling")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required to exercise the npm entry point")
	}
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			root := t.TempDir()
			bin := root
			if local {
				bin = filepath.Join(root, "node_modules", ".bin")
			}
			entry := filepath.Join(root, "node_modules", "@fission-ai", "openspec", "bin", "openspec.js")
			for _, dir := range []string{bin, filepath.Dir(entry)} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			shim := filepath.Join(bin, "openspec.cmd")
			if err := os.WriteFile(shim, []byte("@echo This shell wrapper must not execute\r\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(entry, []byte("console.log(JSON.stringify(process.argv.slice(2)))"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"instructions", "proposal", "--change", `quoted "value" & echo unsafe | %PATH%`, "--json"}
			rt, err := NewOpenSpec(&pipeline.Node{ID: "spec", OpenSpec: &pipeline.OpenSpecSpec{Command: shim, Args: args}}, root)
			if err != nil {
				t.Fatal(err)
			}
			output, err := rt.Invoke(context.Background(), Request{Input: "not a CLI argument"})
			if err != nil {
				t.Fatal(err)
			}
			var actual []string
			if err := json.Unmarshal([]byte(output), &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, args) {
				t.Fatalf("arguments = %#v, want %#v", actual, args)
			}
		})
	}
	t.Run("unknown shim layout", func(t *testing.T) {
		root := t.TempDir()
		shim := filepath.Join(root, "openspec.cmd")
		if err := os.WriteFile(shim, []byte("@echo must not execute\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := openSpecCommand(shim, []string{"list"})
		if err == nil || !strings.Contains(err.Error(), "set openspec.command to node") {
			t.Fatalf("unrecognized shim error = %v", err)
		}
	})
}
