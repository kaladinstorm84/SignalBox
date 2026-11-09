package engine

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

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
