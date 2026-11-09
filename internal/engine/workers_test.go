package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func ownedStore(t *testing.T, dir string) (*Store, string) {
	t.Helper()
	store, err := NewStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	token, err := store.PrepareWorker()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorker(token, 123); err != nil {
		t.Fatal(err)
	}
	return store, token
}

func TestWorkerOwnershipOpeningFencingAndCrash(t *testing.T) {
	dir := t.TempDir()
	owner, token := ownedStore(t, dir)
	execution := &Execution{ID: "owned", Pipeline: "p", Status: StatusWaitingApproval,
		Runs: []*NodeRun{{ID: "gate", Status: StatusWaitingApproval}}}
	if err := owner.Save(execution); err != nil {
		t.Fatal(err)
	}
	observer, err := NewStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	got, err := observer.Get(execution.ID)
	if err != nil || got.Status != StatusWaitingApproval {
		t.Fatalf("opening store interrupted live worker: %+v %v", got, err)
	}
	if err := observer.Save(execution); err == nil {
		t.Fatal("foreign process overwrote owned execution")
	}
	if err := observer.ClaimWorker(token, 456); err == nil {
		t.Fatal("launch capability replay succeeded")
	}
	// Simulate missed heartbeats while the process still has its OS lock.
	if _, err := owner.db.Exec("UPDATE workers SET lease=0 WHERE token=?", token); err != nil {
		t.Fatal(err)
	}
	if err := observer.RecoverWorkers(); err != nil {
		t.Fatal(err)
	}
	if err := owner.RenewWorker(); err == nil {
		t.Fatal("expired owner resurrected its lease")
	}
	execution.Status = StatusCompleted
	if err := owner.Save(execution); err == nil {
		t.Fatal("fenced owner wrote success")
	}
	got, err = observer.Get(execution.ID)
	if err != nil || got.Status != StatusInterrupted || !strings.Contains(got.Error, "owner") {
		t.Fatalf("lost owner classification: %+v %v", got, err)
	}
}

func TestWorkerCrashDetectedByOSLockBeforeLeaseExpiry(t *testing.T) {
	dir := t.TempDir()
	owner, _ := ownedStore(t, dir)
	if err := owner.Save(&Execution{ID: "crash", Pipeline: "p", Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	observer, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	got, err := observer.Get("crash")
	if err != nil || got.Status != StatusInterrupted {
		t.Fatalf("dead worker not interrupted: %+v %v", got, err)
	}
}

func TestWorkerSlotsSharedAndNotReclaimedFromPausedOwner(t *testing.T) {
	dir := t.TempDir()
	first, token := ownedStore(t, dir)
	second, _ := ownedStore(t, dir)
	if err := first.ConfigureRuntimeLimit(1); err != nil {
		t.Fatal(err)
	}
	release, err := first.acquireSlot(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := second.acquireSlot(ctx, 10); err == nil {
		t.Fatal("second worker exceeded project-wide limit")
	}
	if _, err := first.db.Exec("UPDATE workers SET lease=0 WHERE token=?", token); err != nil {
		t.Fatal(err)
	}
	if err := second.RecoverWorkers(); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if _, err := second.acquireSlot(ctx2, 10); err == nil {
		t.Fatal("runtime slot reclaimed while fenced owner still alive")
	}
	release()
	release2, err := second.acquireSlot(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	release2()
	// A crashed owner releases its OS lock; its abandoned slots are reclaimable.
	if _, err := first.db.Exec("INSERT INTO runtime_slots(id,worker) VALUES ('abandoned',?)", token); err != nil {
		t.Fatal(err)
	}
	first.Close()
	if err := second.RecoverWorkers(); err != nil {
		t.Fatal(err)
	}
	release3, err := second.acquireSlot(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	release3()
}

func TestWorkerControlCrashIsAnErrorNotSuccess(t *testing.T) {
	dir := t.TempDir()
	owner, token := ownedStore(t, dir)
	observer, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	result := make(chan error, 1)
	go func() {
		_, err := observer.WorkerCommand(token, WorkerRequest{Op: "cancel", ExecID: "x"})
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		var count int
		if err := owner.db.QueryRow("SELECT COUNT(*) FROM worker_commands").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	owner.Close()
	if err := observer.RecoverWorkers(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("worker crash returned success")
	}
}

func TestWorkerFatalPersistenceCannotLaterWriteSuccess(t *testing.T) {
	store, token := ownedStore(t, t.TempDir())
	execution := &Execution{ID: "fatal", Pipeline: "p", Status: StatusRunning}
	if err := store.Save(execution); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := &Engine{store: store, execs: map[string]*liveExec{
		execution.ID: {exec: execution, ctx: ctx, cancel: cancel, done: make(chan struct{})},
	}}
	eng.mu.Lock()
	eng.failPersistenceLocked(errors.New("simulated disk failure"))
	// A gate or orchestrator canceled by the failure may settle its local
	// counter as completed. That must never overwrite durable running state.
	execution.Status = StatusCompleted
	eng.persistLocked(eng.execs[execution.ID])
	eng.mu.Unlock()
	got, err := store.Get(execution.ID)
	if err != nil || got.Status != StatusRunning {
		t.Fatalf("fatal persistence failure wrote success: %+v %v", got, err)
	}
	if err := store.FinishWorker(token); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(execution.ID)
	if err != nil || got.Status != StatusInterrupted {
		t.Fatalf("failed owner not interrupted: %+v %v", got, err)
	}
}

func TestWorkerExpiredCommandIsNotReplayed(t *testing.T) {
	store, token := ownedStore(t, t.TempDir())
	decisions := make(chan approvalDecision, 1)
	eng := &Engine{store: store, execs: map[string]*liveExec{
		"root": {exec: &Execution{ID: "root"}, approvals: map[string]chan approvalDecision{"gate": decisions}},
	}}
	req := WorkerRequest{Op: "approve", ExecID: "root", RunID: "gate", Decision: true}
	if _, err := store.db.Exec(`INSERT INTO worker_commands(id,worker,request,state,deadline)
		VALUES ('expired',?,?,'pending',0)`, token, mustPayload(req)); err != nil {
		t.Fatal(err)
	}
	if err := store.PollWorker(eng); err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatal("expired approval was applied")
	}
}
