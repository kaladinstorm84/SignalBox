package exporter

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

func pythonExecutable(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python")
	if err != nil {
		t.Skip("Python is required to exercise standalone exports")
	}
	return path
}

func writeTestFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exportTestScript(t *testing.T, dir, yaml string, rc config.RuntimeConfig) string {
	t.Helper()
	p, err := pipeline.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	data, err := Python(p, &config.Config{Runtimes: map[string]config.RuntimeConfig{"agent": rc}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "workflow.py")
	writeTestFile(t, path, string(data))
	return path
}

func runPython(t *testing.T, python, script string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, append([]string{script}, args...)...)
	return cmd.CombinedOutput()
}

func TestPythonExportStreamsAndPreservesArticleRework(t *testing.T) {
	python := pythonExecutable(t)
	dir := t.TempDir()
	helper := filepath.Join(dir, "codex.py")
	writeTestFile(t, helper, `
import json, sys, time
from pathlib import Path
assert "--json" in sys.argv
prompt = sys.stdin.read() if "-" in sys.argv else sys.argv[-1]
with Path("calls.jsonl").open("a", encoding="utf-8") as calls:
    calls.write(json.dumps(prompt) + "\n")
def event(value):
    print(json.dumps(value), flush=True)
event({"type":"thread.started"})
event({"type":"turn.started"})
if prompt.startswith("WRITER"):
    while not Path("release").exists():
        time.sleep(0.01)
    output = "ORIGINAL ARTICLE about exported workflows"
elif prompt.startswith("EDITOR"):
    assert "ORIGINAL ARTICLE" in prompt
    output = "EDITORIAL NOTES: improve the examples"
elif prompt.startswith("REVISER"):
    assert "ORIGINAL ARTICLE" in prompt and "EDITORIAL NOTES" in prompt
    if "rejection notes from gate" in prompt:
        assert "--- context from reviser ---\nDRAFT ONE" in prompt
        assert "Correct the version" in prompt
        output = "DRAFT TWO"
    else:
        output = "DRAFT ONE"
elif prompt.startswith("GATE"):
    assert "additional input from writer" in prompt
    output = json.dumps({"decision":"approve" if "DRAFT TWO" in prompt else "reject", "feedback":"Correct the version"})
else:
    raise RuntimeError("unexpected prompt: " + prompt[:50])
event({"type":"item.completed","item":{"id":"r","type":"reasoning","text":"PRIVATE_REASONING"}})
event({"type":"item.updated","item":{"id":"m","type":"agent_message","text":output[:5]}})
event({"type":"item.completed","item":{"id":"m","type":"agent_message","text":output}})
event({"type":"turn.completed"})
`)
	script := exportTestScript(t, dir, `name: article
params:
  topic: {required: true}
agents:
  - id: writer
    runtime: agent
    prompt: |
      WRITER ${{ params.topic }}
      ${{ params.topic }}
      ${{ params.topic }}
      ${{ params.topic }}
    handoff: [editor]
  - id: editor
    runtime: agent
    prompt: EDITOR
    handoff: [reviser]
  - id: reviser
    runtime: agent
    prompt: REVISER
    additional-input: [writer]
    context: {strategy: own, window: 1}
    handoff: [gate]
  - id: gate
    type: llm_gate
    runtime: agent
    prompt: GATE
    additional-input: [writer]
    reject-to: reviser
    max_reworks: 2
    handoff: [signoff]
  - id: signoff
    type: approval
`, config.RuntimeConfig{Type: "codex", Command: python, Args: []string{"-u", helper}})
	runDir := filepath.Join(dir, "run")
	release := filepath.Join(dir, "release")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, script, "--param", "topic=portable = workflows"+strings.Repeat("x", 7000), "--workdir", dir, "--output-dir", runDir)
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = strings.NewReader("y\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		writeTestFile(t, release, "")
		cancel()
		<-done
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		log, _ := os.ReadFile(filepath.Join(runDir, "0001.log"))
		if strings.Contains(string(log), "[Codex working]") {
			break
		}
		select {
		case <-done:
			t.Fatalf("runner ended before emitting progress: %v\n%s", runErr, stderr.String())
		case <-ctx.Done():
			writeTestFile(t, release, "")
			cancel()
			<-done
			t.Fatalf("no progress saved before test deadline: %v\n%s", runErr, stderr.String())
		case <-ticker.C:
		}
	}
	writeTestFile(t, release, "")
	<-done
	if runErr != nil {
		t.Fatalf("export failed: %v\n%s", runErr, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "DRAFT TWO") || strings.Contains(stdout.String(), "Codex") {
		t.Fatalf("final handoff contains progress or wrong revision: %q", stdout.String())
	}
	log, err := os.ReadFile(filepath.Join(runDir, "0003.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(log) != "[Codex session started]\n[Codex working]\nDRAFT ONE\n[Codex turn completed]\n" {
		t.Fatalf("incorrect streamed message deduplication: %q", log)
	}
	checkpoint, err := os.ReadFile(filepath.Join(runDir, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Status  string            `json:"status"`
		History []json.RawMessage `json:"history"`
	}
	if err := json.Unmarshal(checkpoint, &state); err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" || len(state.History) != 7 || bytes.Contains(checkpoint, []byte("PRIVATE_REASONING")) || strings.Contains(stderr.String(), "PRIVATE_REASONING") {
		t.Fatalf("wrong checkpoint status/history or reasoning leaked: %s", checkpoint)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.jsonl"))
	before := string(calls)
	if output, err := runPython(t, python, script, "--resume", "--output-dir", runDir); err != nil {
		t.Fatalf("resuming completed run: %v\n%s", err, output)
	}
	calls, _ = os.ReadFile(filepath.Join(dir, "calls.jsonl"))
	if string(calls) != before {
		t.Fatal("resuming completed work invoked the CLI again")
	}
}

func TestPythonExportTimeoutAndResumeSkipCompletedSteps(t *testing.T) {
	python := pythonExecutable(t)
	dir := t.TempDir()
	helper := filepath.Join(dir, "agent.py")
	writeTestFile(t, helper, `
import sys, time
from pathlib import Path
prompt = sys.stdin.read()
with Path("calls.txt").open("a") as calls:
    calls.write(prompt.splitlines()[0] + "\n")
if prompt.startswith("SECOND") and not Path("release").exists():
    print("still running", flush=True)
    time.sleep(60)
print("complete")
`)
	script := exportTestScript(t, dir, `name: timeout
agents:
  - id: first
    runtime: agent
    prompt: FIRST
    handoff: [second]
  - id: second
    runtime: agent
    timeout_seconds: 1
    prompt: SECOND
`, config.RuntimeConfig{Type: "cli", Command: python, Args: []string{"-u", helper}, PromptMode: "stdin", TimeoutSeconds: 30})
	runDir := filepath.Join(dir, "run")
	start := time.Now()
	output, err := runPython(t, python, script, "--workdir", dir, "--output-dir", runDir)
	if err == nil || !bytes.Contains(output, []byte("timed out after 1s")) || time.Since(start) > 10*time.Second {
		t.Fatalf("timeout not enforced: %v, elapsed %s\n%s", err, time.Since(start), output)
	}
	writeTestFile(t, filepath.Join(dir, "release"), "")
	output, err = runPython(t, python, script, "--resume", "--output-dir", runDir)
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, output)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls.txt"))
	if err != nil || strings.ReplaceAll(string(calls), "\r\n", "\n") != "FIRST\nSECOND\nSECOND\n" {
		t.Fatalf("completed step was replayed or failed step skipped: %q %v", calls, err)
	}
	if output, err := runPython(t, python, script, "--output-dir", runDir); err == nil || !bytes.Contains(output, []byte("checkpoint already exists")) {
		t.Fatalf("existing run overwritten: %v\n%s", err, output)
	}
}

func TestPythonExportGateErrorsAndBoundedRejection(t *testing.T) {
	python := pythonExecutable(t)
	for _, invalid := range []bool{false, true} {
		name := "rework-limit"
		if invalid {
			name = "invalid-verdict"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			helper := filepath.Join(dir, "agent.py")
			verdict := `{"decision":"reject","feedback":"fix it"}`
			if invalid {
				verdict = `{"decision":"approve","feedback":"ok","extra":true}`
			}
			writeTestFile(t, helper, "import sys\np=sys.stdin.read()\nprint("+strconv.Quote(verdict)+" if p.startswith('GATE') else 'draft')\n")
			script := exportTestScript(t, dir, `name: gate
agents:
  - id: writer
    runtime: agent
    prompt: WRITER
    handoff: [gate]
  - id: gate
    type: llm_gate
    runtime: agent
    prompt: GATE
    reject-to: writer
    max_reworks: 2
`, config.RuntimeConfig{Type: "cli", Command: python, Args: []string{helper}, PromptMode: "stdin"})
			runDir := filepath.Join(dir, "run")
			output, err := runPython(t, python, script, "--workdir", dir, "--output-dir", runDir)
			expected := "rework limit (2)"
			if invalid {
				expected = "invalid verdict"
			}
			if err == nil || !bytes.Contains(output, []byte(expected)) {
				t.Fatalf("gate error became success: %v\n%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(runDir, "result.txt")); !os.IsNotExist(err) {
				t.Fatalf("failed gate produced a final result: %v", err)
			}
		})
	}
}

func TestPythonExportHumanRejectionReworksUpstream(t *testing.T) {
	python := pythonExecutable(t)
	dir := t.TempDir()
	script := exportTestScript(t, dir, `name: review
agents:
  - id: writer
    runtime: agent
    prompt: WRITE
    handoff: [review]
  - id: review
    type: approval
`, config.RuntimeConfig{Type: "cli", Command: python, Args: []string{"-c", "import sys; print(sys.stdin.read())"}, PromptMode: "stdin"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, script, "--input", "source article", "--output-dir", filepath.Join(dir, "run"))
	cmd.Stdin = strings.NewReader("n\nimprove the example\ny\n")
	output, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("--- rejection notes from review ---")) || !bytes.Contains(output, []byte("improve the example")) {
		t.Fatalf("human rejection feedback was lost: %v\n%s", err, output)
	}
}

func TestPythonExportValidatesParametersBeforeInvokingCLI(t *testing.T) {
	python := pythonExecutable(t)
	dir := t.TempDir()
	script := exportTestScript(t, dir, `name: params
params:
  topic: {required: true}
  count: {type: integer, default: 2}
  enabled: {type: boolean, default: true}
agents:
  - id: writer
    runtime: agent
    prompt: Write ${{ params.count }} examples about ${{ params.topic }}
`, config.RuntimeConfig{Type: "cli", Command: python, Args: []string{"-c", "raise RuntimeError('CLI MUST NOT RUN')"}})
	for i, args := range [][]string{
		{},
		{"--param", "unknown=value"},
		{"--param", "topic=article", "--param", "count=1_000"},
		{"--param", "topic=article", "--param", "enabled=yes"},
		{"--param", "topic=article", "--param", "topic=duplicate"},
	} {
		args = append(args, "--output-dir", filepath.Join(dir, strconv.Itoa(i)))
		output, err := runPython(t, python, script, args...)
		if err == nil || !bytes.Contains(output, []byte("parameter")) || bytes.Contains(output, []byte("CLI MUST NOT RUN")) {
			t.Fatalf("invalid parameters reached CLI: %v\n%s", err, output)
		}
	}
}

func TestPythonExportWindowsCheckpointReplacementRetriesWithoutLosingState(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("Windows sharing violations")
	}
	python := pythonExecutable(t)
	dir := t.TempDir()
	script := exportTestScript(t, dir, "name: checkpoint\nagents:\n  - id: writer\n    runtime: agent\n", config.RuntimeConfig{Type: "codex"})
	check := filepath.Join(dir, "check.py")
	writeTestFile(t, check, `
import ctypes, json, os, runpy, sys
from pathlib import Path
runner = runpy.run_path(sys.argv[1])
checkpoint = Path(sys.argv[2]) / "checkpoint.json"
original = os.replace
attempts = 0
def temporary_block(source, destination):
    global attempts
    attempts += 1
    if attempts <= 2:
        raise ctypes.WinError(32)
    original(source, destination)
try:
    os.replace = temporary_block
    runner["save_checkpoint"](checkpoint, {"saved": True})
finally:
    os.replace = original
assert attempts == 3 and json.loads(checkpoint.read_text()) == {"saved": True}
def permanent_block(source, destination):
    raise ctypes.WinError(5)
try:
    os.replace = permanent_block
    try:
        runner["save_checkpoint"](checkpoint, {"saved": False})
    except PermissionError:
        pass
    else:
        raise AssertionError("permanent failure was reported as success")
finally:
    os.replace = original
assert json.loads(checkpoint.read_text()) == {"saved": True}
assert not list(checkpoint.parent.glob(".checkpoint-*"))
`)
	output, err := runPython(t, python, check, script, dir)
	if err != nil {
		t.Fatalf("checkpoint replacement recovery: %v\n%s", err, output)
	}
}

func TestPythonExportRejectsUnsupportedFeatures(t *testing.T) {
	for _, feature := range []string{"parallel", "retry", "node-type", "runtime-type", "parameterized-model"} {
		t.Run(feature, func(t *testing.T) {
			p, err := pipeline.Parse([]byte("name: test\nagents:\n  - id: first\n    runtime: agent\n    prompt: write\n"))
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{"agent": {Type: "codex"}}}
			switch feature {
			case "parallel":
				p.Nodes[0].Handoff = []pipeline.HandoffRule{{To: "a"}, {To: "b"}}
			case "retry":
				p.Nodes[0].Retry = &pipeline.RetryPolicy{Count: 1}
			case "node-type":
				p.Nodes[0].Type = pipeline.TypeOrchestrator
			case "runtime-type":
				cfg.Runtimes["agent"] = config.RuntimeConfig{Type: "openai"}
			case "parameterized-model":
				p.Nodes[0].Model = "${{ params.model }}"
			}
			if _, err := Python(p, cfg); err == nil {
				t.Fatal("unsupported feature silently exported")
			}
		})
	}
}
