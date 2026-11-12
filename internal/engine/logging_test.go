package engine

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestStreamLogFlushesWhileOutputIsIdle(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng, err := New(nil, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	run := &NodeRun{ID: "writer-run", NodeID: "writer", Status: StatusRunning}
	le := &liveExec{
		exec: &Execution{
			ID: "stream-log", Pipeline: "article", Status: StatusRunning,
			CreatedAt: time.Now().UTC(), Runs: []*NodeRun{run},
		},
		done: make(chan struct{}),
	}
	write, finish := eng.streamOutputLogger(le, run, nil, 1)
	defer finish()
	write("Working\n")
	write("A small update")

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		execution, err := store.Get(le.exec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(execution.Runs[0].Logs) == 1 && execution.Runs[0].Logs[0].Text == "Working\nA small update" {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("small update was not saved while output was idle")
		}
	}
}

func TestWorkerCommandFailureIsLogged(t *testing.T) {
	store, token := ownedStore(t, t.TempDir())
	eng, err := New(nil, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	request := WorkerRequest{Op: "start", Start: StartOptions{Pipeline: "missing"}}
	_, err = store.db.Exec(`INSERT INTO worker_commands(id,worker,request,state,deadline)
		VALUES (?,?,?,'pending',(`+workerDBNow+`)+20000000000)`,
		"failed-command", token, mustPayload(request))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	if err := store.PollWorker(eng); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"start command failed-command", `pipeline="missing"`, "failed:", "missing"} {
		if !strings.Contains(text, want) {
			t.Fatalf("worker error log missing %q: %s", want, text)
		}
	}
}
