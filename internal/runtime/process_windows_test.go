//go:build windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

func TestConsoleHelperProcess(t *testing.T) {
	if os.Getenv("SIGNALBOX_CONSOLE_HELPER") != "1" {
		return
	}
	window, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	fmt.Printf("%d", window)
	os.Exit(0)
}

func TestRuntimeStartsWithoutConsole(t *testing.T) {
	t.Setenv("SIGNALBOX_CONSOLE_HELPER", "1")
	rt, err := newCLI("console-test", config.RuntimeConfig{
		Type: "cli", Command: os.Args[0],
		Args: []string{"-test.run=^TestConsoleHelperProcess$"}, PromptMode: "stdin",
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := rt.Invoke(context.Background(), Request{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if output != "0" {
		t.Fatalf("runtime child has a console window: handle %s", output)
	}
}
