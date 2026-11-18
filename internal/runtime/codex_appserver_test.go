package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

// TestCodexAppServerHelperProcess is not a real test: it is a fake
// `codex app-server` speaking JSONL JSON-RPC on stdio. SIGNALBOX_APPSERVER
// selects its scenario: ok, fail or hang.
func TestCodexAppServerHelperProcess(t *testing.T) {
	mode := os.Getenv("SIGNALBOX_APPSERVER")
	if mode == "" {
		return
	}
	in := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(2)
	}
	read := func() map[string]any {
		line, err := in.ReadBytes('\n')
		if err != nil {
			fail("read: %v", err)
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			fail("decode %q: %v", line, err)
		}
		return msg
	}
	expect := func(method string) (map[string]any, map[string]any) {
		msg := read()
		if msg["method"] != method {
			fail("got %v, want %s", msg["method"], method)
		}
		params, _ := msg["params"].(map[string]any)
		return msg, params
	}
	send := func(v map[string]any) { _ = out.Encode(v) }
	notify := func(method string, params map[string]any) { send(map[string]any{"method": method, "params": params}) }
	turn := func(status string, extra map[string]any) map[string]any {
		t := map[string]any{"id": "u1", "status": status, "items": []any{}}
		for k, v := range extra {
			t[k] = v
		}
		return map[string]any{"threadId": "t1", "turn": t}
	}

	if !slices.Contains(os.Args, "app-server") || slices.Contains(os.Args, "--json") || slices.Contains(os.Args, "--model") {
		fail("unexpected args %q", os.Args)
	}
	if model := os.Getenv("SIGNALBOX_APPSERVER_MODEL"); model != "" {
		if i := slices.Index(os.Args, `model="`+model+`"`); i < 1 || os.Args[i-1] != "-c" {
			fail("model override missing from %q", os.Args)
		}
	}
	msg, params := expect("initialize")
	if info, _ := params["clientInfo"].(map[string]any); info["name"] != "signalbox" {
		fail("clientInfo = %v", params["clientInfo"])
	}
	send(map[string]any{"id": msg["id"], "result": map[string]any{"userAgent": "fake"}})
	expect("initialized")
	msg, params = expect("thread/start")
	if params["approvalPolicy"] != "never" {
		fail("approvalPolicy = %v", params["approvalPolicy"])
	}
	send(map[string]any{"id": msg["id"], "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
	msg, params = expect("turn/start")
	input, _ := params["input"].([]any)
	if len(input) != 1 || params["threadId"] != "t1" || !strings.Contains(fmt.Sprint(input[0]), "write the article") {
		fail("turn/start params = %v", params)
	}
	send(map[string]any{"id": msg["id"], "result": turn("inProgress", nil)})
	notify("turn/started", turn("inProgress", nil))

	switch mode {
	case "fail":
		notify("turn/completed", turn("failed", map[string]any{"error": map[string]any{"message": "provider unavailable"}}))
	case "hang":
		msg, params = expect("turn/interrupt")
		if params["threadId"] != "t1" || params["turnId"] != "u1" {
			fail("turn/interrupt params = %v", params)
		}
		_ = os.WriteFile(os.Getenv("SIGNALBOX_APPSERVER_INTERRUPTED"), nil, 0o600)
		send(map[string]any{"id": msg["id"], "result": map[string]any{}})
		notify("turn/completed", turn("interrupted", nil))
	default:
		notify("item/agentMessage/delta", map[string]any{"threadId": "t1", "turnId": "u1", "itemId": "m1", "delta": "Checking sources."})
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("SIGNALBOX_APPSERVER_RELEASE")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				fail("progress was not received before completion")
			}
			time.Sleep(10 * time.Millisecond)
		}
		notify("item/agentMessage/delta", map[string]any{"threadId": "other", "turnId": "x", "itemId": "m9", "delta": "sub-agent chatter"})
		notify("item/reasoning/textDelta", map[string]any{"threadId": "t1", "turnId": "u1", "itemId": "r1", "delta": "private reasoning"})
		notify("item/completed", map[string]any{"threadId": "t1", "turnId": "u1", "item": map[string]any{"id": "m1", "type": "agentMessage", "text": "Checking sources.", "phase": "commentary"}})
		notify("item/started", map[string]any{"threadId": "t1", "turnId": "u1", "item": map[string]any{"id": "c1", "type": "commandExecution", "status": "inProgress"}})
		send(map[string]any{"id": "approval-1", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "t1", "turnId": "u1", "itemId": "c1"}})
		reply := read()
		if result, _ := reply["result"].(map[string]any); reply["id"] != "approval-1" || result["decision"] != "decline" {
			fail("approval reply = %v", reply)
		}
		notify("item/completed", map[string]any{"threadId": "t1", "turnId": "u1", "item": map[string]any{"id": "c1", "type": "commandExecution", "status": "declined"}})
		for _, delta := range []string{"article ", "text"} {
			notify("item/agentMessage/delta", map[string]any{"threadId": "t1", "turnId": "u1", "itemId": "m2", "delta": delta})
		}
		notify("item/completed", map[string]any{"threadId": "t1", "turnId": "u1", "item": map[string]any{"id": "m2", "type": "agentMessage", "text": "article text", "phase": "final_answer"}})
		notify("turn/completed", turn("completed", nil))
	}
	// Like the real server, exit once the client closes stdin.
	_, _ = io.Copy(io.Discard, in)
	os.Exit(0)
}

func appServerHelper(t *testing.T, mode string) StreamingRuntime {
	t.Helper()
	t.Setenv("SIGNALBOX_APPSERVER", mode)
	rt, err := newCLI("codex", config.RuntimeConfig{
		Type: "codex", Command: os.Args[0],
		Args: []string{"-test.run=^TestCodexAppServerHelperProcess$", "--", "app-server"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt.(StreamingRuntime)
}

func TestCodexAppServerStreamsActivityAndHandsOffFinalAnswer(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv("SIGNALBOX_APPSERVER_RELEASE", release)
	t.Setenv("SIGNALBOX_APPSERVER_MODEL", "gpt-test")
	rt := appServerHelper(t, "ok")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var streamed strings.Builder
	released := false
	output, err := rt.InvokeStream(ctx, Request{SystemPrompt: "You write.", Input: "write the article", Model: "gpt-test"}, func(text string) {
		streamed.WriteString(text)
		if !released && strings.Contains(text, "Checking sources.") {
			released = true
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	if err != nil || output != "article text" {
		t.Fatalf("output = %q, error = %v, log = %q", output, err, streamed.String())
	}
	log := streamed.String()
	for _, want := range []string{"Codex working", "Checking sources.\n", "[command execution started]", "command approval declined", "[command execution declined]", "article text\n", "Codex turn completed"} {
		if !strings.Contains(log, want) {
			t.Fatalf("log is missing %q: %q", want, log)
		}
	}
	if strings.Contains(log, "private reasoning") || strings.Contains(log, "sub-agent chatter") {
		t.Fatalf("log exposed reasoning or another thread: %q", log)
	}
}

func TestCodexAppServerFailedTurnIsAnError(t *testing.T) {
	rt := appServerHelper(t, "fail")
	output, err := rt.(Runtime).Invoke(context.Background(), Request{Input: "write the article"})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") || output != "" {
		t.Fatalf("output = %q, error = %v", output, err)
	}
}

func TestCodexAppServerCancellationInterruptsTurn(t *testing.T) {
	interrupted := filepath.Join(t.TempDir(), "interrupted")
	t.Setenv("SIGNALBOX_APPSERVER_INTERRUPTED", interrupted)
	rt := appServerHelper(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	_, err := rt.InvokeStream(ctx, Request{Input: "write the article"}, func(text string) {
		if strings.Contains(text, "Codex working") {
			cancel()
		}
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(interrupted); statErr != nil {
		t.Fatal("turn/interrupt was not sent before stopping the server")
	}
	if elapsed := time.Since(start); elapsed > appServerGrace {
		t.Fatalf("cancellation took %s", elapsed)
	}
}
