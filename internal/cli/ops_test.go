package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
	"github.com/kaladinstorm84/SignalBox/internal/server"
)

const opsPipeline = `
name: demo
entry: draft
agents:
  - id: draft
    runtime: mock
    handoff: [review]
  - id: review
    type: approval
    handoff: [fix]
  - id: fix
    type: manual_edit
    handoff: [publish]
  - id: publish
    runtime: flaky
`

// TestExecutionCommandsDriveTheServerAPI runs the CLI against the real server
// handler, so the CLI and web UI share behaviour.
func TestExecutionCommandsDriveTheServerAPI(t *testing.T) {
	p, err := pipeline.Parse([]byte(opsPipeline))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	current := p
	load := func() (map[string]*pipeline.Pipeline, error) {
		mu.Lock()
		defer mu.Unlock()
		return map[string]*pipeline.Pipeline{"demo": current}, nil
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	registry.Register(&runtime.Mock{RuntimeName: "flaky", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return "", fmt.Errorf("upstream down")
		}
		return "published", nil
	}})
	eng, err := engine.New(map[string]*pipeline.Pipeline{"demo": p}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(eng, "test", load, nil, nil).Handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := cmdOps(args[0], append(args[1:], "--addr", addr), &out)
		return out.String(), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("signalbox %s: %v", strings.Join(args, " "), err)
		}
		return out
	}
	waitFor := func(id string, want engine.Status) *engine.Execution {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			exec, err := eng.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range exec.Runs {
				if r.Status == want && !want.Terminal() {
					return exec
				}
			}
			if exec.Status == want {
				return exec
			}
			if time.Now().After(deadline) {
				t.Fatalf("execution %s never reached %s: %+v", id, want, exec.Status)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	startedID := func(out string) string {
		t.Helper()
		fields := strings.Fields(out)
		if len(fields) < 2 || fields[0] != "started" {
			t.Fatalf("unexpected start output %q", out)
		}
		return fields[1]
	}

	id := startedID(must("start", "demo", "--input", "hello"))
	waitFor(id, engine.StatusWaitingApproval)
	if out := must("approve", id, "--comment", "looks good", "--by", "matt"); !strings.Contains(out, "approved by matt") {
		t.Fatalf("approve output %q", out)
	}
	waitFor(id, engine.StatusWaitingManualEdit)
	if _, err := run("edit", id); err == nil {
		t.Fatal("edit without output accepted")
	}
	must("edit", id, "--output", "fixed draft")
	waitFor(id, engine.StatusWaitingRetry)
	if _, err := run("approve", id); err == nil || !strings.Contains(err.Error(), "no run in waiting_approval") {
		t.Fatalf("approve with nothing waiting: %v", err)
	}
	must("retry", id)
	exec := waitFor(id, engine.StatusCompleted)
	if exec.Status != engine.StatusCompleted {
		t.Fatalf("status %s", exec.Status)
	}
	for _, r := range exec.Runs {
		if r.NodeID == "review" && (r.ApprovedBy != "matt" || r.Comment != "looks good") {
			t.Fatalf("approval metadata %+v", r)
		}
	}

	show := must("show", id)
	for _, want := range []string{"Pipeline:  demo", "completed", "publish", "review"} {
		if !strings.Contains(show, want) {
			t.Fatalf("show missing %q:\n%s", want, show)
		}
	}
	if out := must("show", id, "--json"); !strings.Contains(out, `"pipeline": "demo"`) {
		t.Fatalf("show --json:\n%s", out)
	}
	if _, err := run("restart", id); err == nil || !strings.Contains(err.Error(), "no failed, interrupted, or canceled run") {
		t.Fatalf("auto restart of completed execution: %v", err)
	}
	restarted := startedID(must("restart", id, "--node", "publish"))
	waitFor(restarted, engine.StatusCompleted)
	if out := must("show", restarted); !strings.Contains(out, "Restarted: from "+id) || !strings.Contains(out, "reused from "+id) {
		t.Fatalf("restart show:\n%s", out)
	}

	changed, err := pipeline.Parse([]byte(strings.Replace(opsPipeline, "runtime: flaky", "runtime: flaky\n    prompt: changed", 1)))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	current = changed
	mu.Unlock()
	if _, err := run("restart", id, "--node", "publish"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("changed definition restart: %v", err)
	}
	forced := startedID(must("restart", id, "--node", "publish", "--force"))
	waitFor(forced, engine.StatusCompleted)
	if out := must("show", forced); !strings.Contains(out, "forced") || !strings.Contains(out, "modified agents: publish") {
		t.Fatalf("forced show:\n%s", out)
	}

	rejected := startedID(must("rerun", id))
	waitFor(rejected, engine.StatusWaitingApproval)
	must("reject", rejected, "--comment", "no")
	// Rejecting an approval reworks the previous step, which asks again.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		exec, _ := eng.Get(rejected)
		reviews := 0
		for _, r := range exec.Runs {
			if r.NodeID == "review" {
				reviews++
			}
		}
		if reviews == 2 && exec.Runs[len(exec.Runs)-1].Status == engine.StatusWaitingApproval {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rejection did not rework: %+v", exec.Runs)
		}
	}
	canceled := rejected
	must("cancel", canceled)
	waitFor(canceled, engine.StatusCanceled)

	list := must("list")
	for _, want := range []string{id, restarted, canceled, "PIPELINE"} {
		if !strings.Contains(list, want) {
			t.Fatalf("list missing %q:\n%s", want, list)
		}
	}
	must("delete", canceled)
	if _, err := run("show", canceled); err == nil {
		t.Fatal("deleted execution still shown")
	}
	if _, err := run("show"); err == nil {
		t.Fatal("show without id accepted")
	}
	if _, err := run("retry", id, "--bogus"); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestStartWithFilesUploadsMultipart(t *testing.T) {
	p, err := pipeline.Parse([]byte("name: up\nfiles: {required: true}\nparams:\n  main: {type: file}\nagents:\n  - id: a\n    runtime: mock\n    files: inline\n"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := engine.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng, err := engine.New(map[string]*pipeline.Pipeline{"up": p}, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	load := func() (map[string]*pipeline.Pipeline, error) { return map[string]*pipeline.Pipeline{"up": p}, nil }
	srv := httptest.NewServer(server.New(eng, "test", load, nil, nil).Handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	dir := t.TempDir()
	path := filepath.Join(dir, "orders.csv")
	if err := os.WriteFile(path, []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cmdOps("start", []string{"up", "--file", path, "--param", "main=orders.csv", "--addr", addr}, &out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out.String())
	if len(fields) < 2 || !strings.Contains(out.String(), "1 file(s) attached") {
		t.Fatalf("start output %q", out.String())
	}
	x, err := eng.Get(fields[1])
	if err != nil || len(x.Files) != 1 || x.Files[0].Name != "orders.csv" || x.Files[0].Size != 8 || x.Params["main"] != "orders.csv" {
		t.Fatalf("execution = %+v %v", x, err)
	}

	out.Reset()
	if err := cmdOps("start", []string{"up", "--input", "x", "--addr", addr}, &out); err == nil || !strings.Contains(err.Error(), "requires at least one file") {
		t.Fatalf("start without files: %v", err)
	}
	if err := cmdOps("start", []string{"up", "--file", filepath.Join(dir, "missing.csv"), "--addr", addr}, &out); err == nil {
		t.Fatal("missing local file accepted")
	}
}
