package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/engine"
)

// This builds and drives the real binary, not a second in-process engine.
// Scratch files stay in the repository; the tracked signalbox.exe is untouched.
func TestDetachedWorkerBinaryLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("binary subprocess integration")
	}
	work, err := os.MkdirTemp(".", ".worker-integration-")
	if err != nil {
		t.Fatal(err)
	}
	work, _ = filepath.Abs(work)
	defer func() {
		if t.Failed() {
			_ = filepath.Walk(work, func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && strings.HasSuffix(path, ".log") {
					content, _ := os.ReadFile(path)
					t.Logf("%s:\n%s", path, content)
				}
				return nil
			})
		}
		if err := os.RemoveAll(work); err != nil {
			t.Errorf("cleaning subprocess test: %v", err)
		}
	}()
	exe := filepath.Join(work, "signalbox-test.exe")
	build := exec.Command("go", "build", "-o", exe, filepath.Join("..", "..", "cmd", "signalbox"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	var release atomic.Bool
	var entered atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	var failedOnce atomic.Bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		if strings.Contains(string(body), "hold-runtime") {
			entered.Add(1)
			for !release.Load() {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
		if strings.Contains(string(body), "fail-once") && !failedOnce.Swap(true) {
			http.Error(w, "mock transient error", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"mock result"}}]}`)
	}))
	defer mock.Close()
	defer release.Store(true)
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("signalbox.yaml", fmt.Sprintf(`version: 1
project: detached-test
state_dir: .signalbox
max_concurrent_calls: 1
runtimes:
  mock:
    type: openai
    model: mock
    base_url: %s
    api_key_env: SIGNALBOX_WORKER_TEST_KEY
watchers:
  - name: binary-watcher
    command: %q
    args: [version]
    interval: 1h
    pipeline: watched
`, mock.URL, exe))
	parentYAML := `name: parent
agents:
  - id: prepare
    runtime: mock
    prompt: prepare
    handoff: [child]
  - id: child
    type: pipeline
    pipeline: child
    files: inherit
    handoff: [finish]
  - id: finish
    runtime: mock
    prompt: finish
`
	write(filepath.Join("pipelines", "parent.yaml"), parentYAML)
	write(filepath.Join("pipelines", "child.yaml"), `name: child
agents:
  - id: review
    type: approval
    handoff: [edit]
  - id: edit
    type: manual_edit
`)
	write(filepath.Join("pipelines", "quick.yaml"), `name: quick
agents:
  - id: quick
    runtime: mock
    prompt: quick
`)
	write(filepath.Join("pipelines", "watched.yaml"), `name: watched
agents:
  - id: review
    type: approval
`)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	base := "http://" + addr
	client := &http.Client{Timeout: 25 * time.Second}
	var serve *exec.Cmd
	var serveDone chan error
	startServe := func() {
		t.Helper()
		logFile, err := os.OpenFile(filepath.Join(work, "serve.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer logFile.Close()
		serve = exec.Command(exe, "serve", "--addr", addr)
		serve.Dir = work
		serve.Env = append(os.Environ(), "SIGNALBOX_WORKER_TEST_KEY=local-test")
		serve.Stdout, serve.Stderr = logFile, logFile
		if err := serve.Start(); err != nil {
			t.Fatal(err)
		}
		serveDone = make(chan error, 1)
		go func(cmd *exec.Cmd, done chan error) { done <- cmd.Wait() }(serve, serveDone)
		waitBinary(t, "serve readiness", func() bool {
			resp, err := client.Get(base + "/api/meta")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		})
	}
	stopServe := func() {
		if serve != nil {
			_ = serve.Process.Kill()
			<-serveDone
			serve = nil
		}
	}
	defer stopServe()
	startServe()
	db, err := sql.Open("sqlite", filepath.Join(work, ".signalbox", "executions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec("PRAGMA busy_timeout=5000")
	defer func() {
		// Exact PIDs, all from this test's private state; never name-based kill.
		workerPIDs := map[int]bool{}
		rows, err := db.Query("SELECT pid FROM workers WHERE pid>0 AND state='running'")
		if err == nil {
			for rows.Next() {
				var pid int
				if rows.Scan(&pid) == nil {
					workerPIDs[pid] = true
				}
			}
			rows.Close()
		}
		for pid := range workerPIDs {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
				_, _ = process.Wait()
			}
		}
	}()
	request := func(method, path string, body any, status int, out any) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		payload, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != status {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, status, payload)
		}
		if out != nil {
			if err := json.Unmarshal(payload, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	start := func(pipeline, input string) string {
		t.Helper()
		var result struct{ ID string }
		request("POST", "/api/executions", map[string]string{"pipeline": pipeline, "input": input}, 201, &result)
		return result.ID
	}
	get := func(id string) *engine.Execution {
		t.Helper()
		var execution engine.Execution
		request("GET", "/api/executions/"+id, nil, 200, &execution)
		return &execution
	}
	pidFor := func(id string) int {
		t.Helper()
		var pid int
		if err := db.QueryRow(`SELECT w.pid FROM workers w JOIN execution_owners o ON o.worker=w.token
			WHERE o.execution=?`, id).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if pid == serve.Process.Pid || pid == 0 {
			t.Fatalf("execution not isolated: worker=%d serve=%d", pid, serve.Process.Pid)
		}
		return pid
	}
	var watched string
	waitBinary(t, "watcher-owned execution", func() bool {
		return db.QueryRow("SELECT id FROM executions WHERE pipeline='watched'").Scan(&watched) == nil
	})
	pidFor(watched)
	request("POST", "/api/executions/"+watched+"/cancel", nil, 200, nil)
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	_ = writer.WriteField("pipeline", "parent")
	_ = writer.WriteField("input", "hold-runtime")
	part, _ := writer.CreateFormFile("file", "notes.txt")
	_, _ = io.WriteString(part, "persistent uploaded content")
	_ = writer.Close()
	req, _ := http.NewRequest("POST", base+"/api/executions", &upload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded struct{ ID string }
	payload, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 || json.Unmarshal(payload, &uploaded) != nil {
		t.Fatalf("upload launch failed: %d %s", resp.StatusCode, payload)
	}
	parent := uploaded.ID
	if staged, _ := filepath.Glob(filepath.Join(work, ".signalbox", "workers", "*.uploads")); len(staged) != 0 {
		t.Fatal("acknowledged launch left staged upload files")
	}
	parentPID := pidFor(parent)
	waitBinary(t, "long-running mock invocation", func() bool { return entered.Load() == 1 })
	queued := start("quick", "hold-runtime")
	if pidFor(queued) == parentPID {
		t.Fatal("top-level executions shared a worker")
	}
	time.Sleep(150 * time.Millisecond)
	if entered.Load() != 1 {
		t.Fatal("second worker exceeded project-wide runtime concurrency")
	}
	stopServe()
	release.Store(true)
	waitBinary(t, "worker progress with serve stopped", func() bool {
		var status string
		err := db.QueryRow("SELECT status FROM executions WHERE id=?", queued).Scan(&status)
		var childCount int
		_ = db.QueryRow("SELECT COUNT(*) FROM executions WHERE pipeline='child' AND waiting_approvals=1").Scan(&childCount)
		return err == nil && status == "completed" && childCount == 1
	})
	if maximum.Load() != 1 {
		t.Fatalf("maximum runtime concurrency=%d, want 1", maximum.Load())
	}
	// Opening foreground run must not interrupt detached owners.
	run := exec.Command(exe, "run", "quick", "--input", "foreground", "--yes")
	run.Dir = work
	run.Env = append(os.Environ(), "SIGNALBOX_WORKER_TEST_KEY=local-test")
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("foreground run: %v\n%s", err, output)
	}
	startServe()
	execution := get(parent)
	if len(execution.Surfaced) != 1 || execution.Surfaced[0].Run.Status != engine.StatusWaitingApproval {
		t.Fatalf("child approval not surfaced after restart: %+v", execution)
	}
	childID := execution.Surfaced[0].Execution
	if len(execution.Files) != 1 || len(get(childID).Files) != 1 {
		t.Fatal("worker lost uploaded/inherited files")
	}
	download, err := client.Get(base + "/api/executions/" + parent + "/files/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(download.Body)
	download.Body.Close()
	if string(content) != "persistent uploaded content" {
		t.Fatalf("upload download after restart: %q", content)
	}
	gateID := execution.Surfaced[0].Run.ID
	stopServe()
	startServe()
	execution = get(parent)
	if len(execution.Surfaced) != 1 || execution.Surfaced[0].Run.ID != gateID {
		t.Fatal("stopping serve interrupted a pending gate")
	}
	if pidFor(childID) != parentPID {
		t.Fatal("child escaped its parent's owner process")
	}
	var dashboard engine.Dashboard
	request("GET", "/api/dashboard", nil, 200, &dashboard)
	if dashboard.InProgress != 1 || dashboard.WaitingApprovals != 1 {
		t.Fatalf("dashboard lost detached execution: %+v", dashboard)
	}
	var chunk engine.Chunk
	request("POST", "/api/executions/"+parent+"/chunks", map[string]string{"title": "durable chunk"}, 201, &chunk)
	request("POST", "/api/executions/"+parent+"/chunks/"+chunk.ID+"/done", nil, 200, nil)
	if len(get(parent).Chunks) != 1 || get(parent).Chunks[0].Status != engine.ChunkDone {
		t.Fatal("chunk mutation did not reach owner")
	}
	request("DELETE", "/api/executions/"+parent+"/chunks/"+chunk.ID, nil, 200, nil)
	request("POST", "/api/executions/"+parent+"/approve",
		map[string]any{"run_id": execution.Surfaced[0].Run.ID, "approved": true}, 200, nil)
	waitBinary(t, "child manual edit", func() bool {
		execution = get(parent)
		return len(execution.Surfaced) == 1 && execution.Surfaced[0].Run.Status == engine.StatusWaitingManualEdit
	})
	request("POST", "/api/executions/"+parent+"/manual-edit",
		map[string]string{"run_id": execution.Surfaced[0].Run.ID, "output": "edited"}, 200, nil)
	waitBinary(t, "parent completion", func() bool { return get(parent).Status == engine.StatusCompleted })
	waitBinary(t, "child completion", func() bool { return get(childID).Status == engine.StatusCompleted })
	// Rerun and recovery are fresh top-level owners, not work in serve or
	// the source execution's process. Definition conflicts retain their type.
	var rerun struct{ ID string }
	request("POST", "/api/executions/"+parent+"/rerun", map[string]any{}, 201, &rerun)
	if len(get(rerun.ID).Files) != 1 {
		t.Fatal("rerun lost uploaded files")
	}
	if pidFor(rerun.ID) == parentPID {
		t.Fatal("rerun reused source worker")
	}
	waitBinary(t, "rerun child gate", func() bool { return len(get(rerun.ID).Surfaced) == 1 })
	request("POST", "/api/executions/"+rerun.ID+"/cancel", nil, 200, nil)
	waitBinary(t, "cancellation", func() bool { return get(rerun.ID).Status == engine.StatusCanceled })
	waitBinary(t, "child cancellation", func() bool {
		var count int
		_ = db.QueryRow("SELECT COUNT(*) FROM executions WHERE pipeline='child' AND status='canceled'").Scan(&count)
		return count == 1
	})
	retryID := start("quick", "fail-once")
	pidFor(retryID)
	waitBinary(t, "runtime failure awaiting retry", func() bool {
		for _, run := range get(retryID).Runs {
			if run.Status == engine.StatusWaitingRetry {
				return true
			}
		}
		return false
	})
	retryExecution := get(retryID)
	request("POST", "/api/executions/"+retryID+"/retry", map[string]string{"run_id": retryExecution.Runs[0].ID}, 200, nil)
	waitBinary(t, "retried worker completion", func() bool { return get(retryID).Status == engine.StatusCompleted })
	write(filepath.Join("pipelines", "parent.yaml"), strings.Replace(parentYAML, "prompt: prepare", "prompt: changed", 1))
	var conflict map[string]any
	request("POST", "/api/executions/"+rerun.ID+"/restart",
		map[string]string{"node_id": "prepare"}, 409, &conflict)
	if conflict["definition_changed"] != true {
		t.Fatalf("definition conflict lost: %+v", conflict)
	}
	var restarted struct{ ID string }
	request("POST", "/api/executions/"+rerun.ID+"/restart",
		map[string]any{"node_id": "prepare", "force": true}, 201, &restarted)
	pidFor(restarted.ID)
	waitBinary(t, "restarted child gate", func() bool { return len(get(restarted.ID).Surfaced) == 1 })
	// A real worker crash is interrupted promptly using its OS ownership
	// lock, even before its durable lease would otherwise expire.
	crashedPID := pidFor(restarted.ID)
	process, _ := os.FindProcess(crashedPID)
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = process.Wait()
	waitBinary(t, "worker crash classification", func() bool { return get(restarted.ID).Status == engine.StatusInterrupted })
	if get(restarted.ID).Error == "" {
		t.Fatal("worker failure was success-shaped")
	}
	request("POST", "/api/executions/"+restarted.ID+"/cancel", nil, 409, nil)
	// Serve's launch-time settings remain authoritative until serve restarts,
	// even if someone changes state_dir in the configuration on disk.
	configPath := filepath.Join(work, "signalbox.yaml")
	originalConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	write("signalbox.yaml", strings.Replace(string(originalConfig), "state_dir: .signalbox", "state_dir: .different-state", 1))
	snapshotID := start("quick", "configuration snapshot")
	pidFor(snapshotID)
	waitBinary(t, "configuration-snapshot completion", func() bool { return get(snapshotID).Status == engine.StatusCompleted })
	write("signalbox.yaml", string(originalConfig))
	// The internal command cannot be used as an externally supplied API.
	internal := exec.Command(exe, "_worker")
	internal.Dir = work
	if output, err := internal.CombinedOutput(); err == nil || !bytes.Contains(output, []byte("internal command")) {
		t.Fatalf("internal worker accepted unauthenticated invocation: %v %s", err, output)
	}
}

func waitBinary(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
