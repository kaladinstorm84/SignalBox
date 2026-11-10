package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

// TestOpenSpecCommandProcess stands in for the external CLI, verifying that
// dependency files exist before returning the next artifact's instructions.
func TestOpenSpecCommandProcess(t *testing.T) {
	if os.Getenv("SIGNALBOX_ENGINE_OPENSPEC_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	fail := func(err error) {
		fmt.Fprint(os.Stderr, err)
		os.Exit(1)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || len(input) > 0 {
		fail(fmt.Errorf("command received unexpected stdin: %q (%v)", input, err))
	}
	log, err := os.OpenFile("commands.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err)
	}
	if _, err := fmt.Fprintln(log, strings.Join(args, " ")); err != nil {
		fail(err)
	}
	if err := log.Close(); err != nil {
		fail(err)
	}
	if args[0] == "fail" {
		fail(fmt.Errorf("OpenSpec validation failed"))
	}
	change := ""
	for i, arg := range args {
		if arg == "--change" && i+1 < len(args) {
			change = args[i+1]
		}
	}
	if args[0] == "new" {
		change = args[2]
	}
	if args[0] == "validate" {
		change = args[1]
	}
	dir := filepath.Join("openspec", "changes", change)
	dependencies := map[string][]string{
		"proposal": nil,
		"specs":    {"proposal.md"},
		"design":   {"proposal.md"},
		"tasks":    {"proposal.md", "specs/export/spec.md", "design.md"},
		"apply":    {"proposal.md", "specs/export/spec.md", "design.md", "tasks.md"},
	}
	check := func(files []string) {
		for _, file := range files {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(file))); err != nil {
				fail(err)
			}
		}
	}
	artifact := ""
	switch args[0] {
	case "new":
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fail(err)
		}
	case "instructions":
		artifact = args[1]
		check(dependencies[artifact])
	case "validate":
		check(dependencies["apply"])
	default:
		fail(fmt.Errorf("unexpected command: %v", args))
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"artifact": artifact, "change": change, "valid": true,
	}); err != nil {
		fail(err)
	}
	os.Exit(0)
}

func TestOpenSpecExamplePlansReworksApprovesAndImplements(t *testing.T) {
	sampleRoot := filepath.Join("..", "..", "testproject")
	sample := filepath.Join(sampleRoot, "pipelines", "openspec-change.yaml")
	data, err := os.ReadFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.ParseFile(data, sample, pipeline.Options{Root: sampleRoot})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("SIGNALBOX_ENGINE_OPENSPEC_HELPER", "1")
	for i := range p.Nodes {
		if spec := p.Nodes[i].OpenSpec; spec != nil {
			spec.Command = os.Args[0]
			spec.Args = append([]string{"-test.run=^TestOpenSpecCommandProcess$", "--"}, spec.Args...)
		}
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"codex": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := map[string]int{}
	registry.Register(&runtime.Mock{RuntimeName: "codex", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		artifact := ""
		for _, candidate := range []string{"proposal", "specs", "design", "tasks"} {
			if strings.Contains(req.SystemPrompt, candidate+" artifact") {
				artifact = candidate
				break
			}
		}
		if artifact == "" {
			artifact = "apply"
		}
		calls[artifact]++
		if !strings.Contains(req.Input, `"artifact":"`+artifact+`"`) {
			return "", fmt.Errorf("%s did not receive its CLI instructions: %s", artifact, req.Input)
		}
		if artifact == "proposal" {
			if !strings.Contains(req.Input, "CSV export with no secrets") {
				return "", fmt.Errorf("proposal lost original requirements")
			}
			if calls[artifact] == 2 && !strings.Contains(req.Input, "Add a regression test") {
				return "", fmt.Errorf("proposal lost rejection notes")
			}
		}
		if artifact == "apply" && !strings.Contains(req.Input, "Keep exports read-only") {
			return "", fmt.Errorf("implementer lost approval notes")
		}
		files := map[string]string{
			"proposal": "proposal.md", "specs": "specs/export/spec.md",
			"design": "design.md", "tasks": "tasks.md", "apply": "implemented.txt",
		}
		path := filepath.Join(root, "openspec", "changes", "add-export", filepath.FromSlash(files[artifact]))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(artifact+" complete"), 0o600); err != nil {
			return "", err
		}
		return "## Planning pack\n" + artifact + " complete", nil
	}})
	eng, err := NewWithLimit(map[string]*pipeline.Pipeline{p.Name: p}, registry, nil, 1, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.StartWithParams(p.Name, "CSV export with no secrets", map[string]string{"change": "add-export"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Cancel(id) })
	waitApproval := func(previous string) *NodeRun {
		t.Helper()
		var result *NodeRun
		waitFor(t, "OpenSpec approval", func() bool {
			if retries := eng.WaitingRetries(id); len(retries) > 0 {
				t.Fatalf("unexpected failure: %s: %s", retries[0].NodeID, retries[0].Error)
			}
			for _, run := range eng.WaitingApprovals(id) {
				if run.ID != previous {
					result = run
					return true
				}
			}
			return false
		})
		return result
	}
	gate := waitApproval("")
	if !strings.Contains(gate.Input, "Planning pack") || !strings.Contains(gate.Input, `"valid":true`) {
		t.Fatalf("approval payload = %s", gate.Input)
	}
	mu.Lock()
	applyCalls := calls["apply"]
	mu.Unlock()
	if applyCalls != 0 {
		t.Fatal("implementation ran before approval")
	}
	if err := eng.Approve(id, gate.ID, false, "reviewer", "Add a regression test"); err != nil {
		t.Fatal(err)
	}
	gate = waitApproval(gate.ID)
	if err := eng.Approve(id, gate.ID, true, "reviewer", "Keep exports read-only"); err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("execution = %s: %s", execution.Status, execution.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, artifact := range []string{"proposal", "specs", "design", "tasks", "apply"} {
		want := 2
		if artifact == "apply" {
			want = 1
		}
		if calls[artifact] != want {
			t.Errorf("%s calls = %d, want %d", artifact, calls[artifact], want)
		}
	}
	log, err := os.ReadFile(filepath.Join(root, "commands.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "new change") != 1 || strings.Count(string(log), "validate add-export") != 3 {
		t.Fatalf("unexpected command sequence:\n%s", log)
	}
	if !restartableType(pipeline.TypeOpenSpec) || !contextSourceType(pipeline.TypeOpenSpec) {
		t.Fatal("OpenSpec outputs must support restart and downstream context")
	}
}

func TestOpenSpecFailureWaitsForRetry(t *testing.T) {
	t.Setenv("SIGNALBOX_ENGINE_OPENSPEC_HELPER", "1")
	p, err := pipeline.Parse([]byte("name: p\nagents:\n  - id: validate\n    type: openspec\n    openspec: {args: [fail]}\n    retry: {count: 1}\n    handoff: [consumer]\n  - id: consumer\n    runtime: mock\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Nodes[0].OpenSpec.Command = os.Args[0]
	p.Nodes[0].OpenSpec.Args = []string{"-test.run=^TestOpenSpecCommandProcess$", "--", "fail"}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	eng, err := NewWithLimit(map[string]*pipeline.Pipeline{p.Name: p}, registry, nil, 1, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.Start("p", "must not become a CLI argument")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Cancel(id) })
	run := waitForRetry(t, eng, id)
	if run.NodeID != "validate" || !strings.Contains(run.Error, "OpenSpec validation failed") {
		t.Fatalf("failed run = %+v", run)
	}
	log, err := os.ReadFile(filepath.Join(root, "commands.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "fail") != 2 {
		t.Fatalf("retry calls = %s", log)
	}
	if err := eng.Cancel(id); err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	for _, run := range execution.Runs {
		if run.NodeID == "consumer" && run.Status == StatusCompleted {
			t.Fatal("failed OpenSpec command handed off")
		}
	}
}
