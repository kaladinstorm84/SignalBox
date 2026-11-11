package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WorkerLease is deliberately longer than SQLite's busy timeout. An owner
// that cannot renew is fenced: it may never write or accept controls again.
const WorkerLease = 30 * time.Second

const workerDBNow = "CAST(unixepoch('subsec') * 1000000000 AS INTEGER)"

// WorkerRequest is the private, durable control protocol. It is not an HTTP API.
type WorkerRequest struct {
	Op       string
	ExecID   string
	RunID    string
	Text     string
	Actor    string
	Decision bool
	Start    StartOptions
	Restart  RestartOptions
	Uploads  []string
}

// WorkerResponse carries a durable acknowledgement; Changed preserves the
// structured restart conflict that callers need for a force confirmation.
type WorkerResponse struct {
	ID      string
	Chunk   Chunk
	Error   string
	Changed *DefinitionChangedError
}

func mustPayload(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// checkWriter fences execution writes by owner and lease within the same
// transaction as the save. A heartbeat check outside that transaction is stale.
func (s *Store) checkWriter(tx *sql.Tx, id string) error {
	// Acquire the write lock before reading, avoiding deferred-transaction
	// read-to-write upgrades (SQLITE_BUSY) across independent processes.
	if _, err := tx.Exec("UPDATE workers SET lease=lease WHERE token=?", s.owner); err != nil {
		return err
	}
	var owner string
	err := tx.QueryRow("SELECT worker FROM execution_owners WHERE execution=?", id).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if owner != "" && owner != s.owner {
		return fmt.Errorf("execution %s belongs to another process", id)
	}
	if s.owner == "" {
		return nil
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM workers WHERE token=? AND state='running' AND lease>?`,
		s.owner, time.Now().UnixNano()).Scan(&active); err != nil {
		return err
	}
	if active != 1 {
		return fmt.Errorf("execution owner has lost its lease")
	}
	_, err = tx.Exec("INSERT OR IGNORE INTO execution_owners(execution,worker) VALUES (?,?)", id, s.owner)
	return err
}

// PrepareWorker records a launch before spawning so a failed launch is visible
// and cannot leave an unowned execution running.
func (s *Store) PrepareWorker() (string, error) {
	token := randHex(32)
	_, err := s.db.Exec("INSERT INTO workers(token,lease,state) VALUES (?,?,'starting')",
		token, time.Now().Add(WorkerLease).UnixNano())
	return token, err
}

// ClaimWorker is a one-shot launch handshake; a token cannot be replayed.
func (s *Store) ClaimWorker(token string, pid int) error {
	if len(token) != 64 || strings.Trim(token, "0123456789abcdef") != "" {
		return fmt.Errorf("invalid worker capability")
	}
	lock, err := s.openOwnerLock(token)
	if err != nil {
		return err
	}
	if err := lockWorkerFile(lock); err != nil {
		lock.Close()
		return fmt.Errorf("worker is already alive: %w", err)
	}
	claimed := false
	defer func() {
		if !claimed {
			lock.Close()
		}
	}()
	result, err := s.db.Exec(`UPDATE workers SET state='running',pid=?,lease=(`+workerDBNow+`)+?
		WHERE token=? AND state='starting' AND lease>(`+workerDBNow+`)`, pid, WorkerLease.Nanoseconds(), token)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("invalid, expired, or already claimed worker launch")
	}
	s.owner = token
	s.ownerLock = lock
	claimed = true
	return nil
}

// RenewWorker never resurrects an expired owner.
func (s *Store) RenewWorker() error {
	result, err := s.db.Exec(`UPDATE workers SET lease=(`+workerDBNow+`)+? WHERE token=? AND state='running' AND lease>(`+workerDBNow+`)`,
		WorkerLease.Nanoseconds(), s.owner)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("worker lease expired")
	}
	return nil
}

// FinishWorker fences further writes, including those from a hung old process.
func (s *Store) FinishWorker(token string) error {
	_, err := s.db.Exec("UPDATE workers SET state='stopped',lease=0 WHERE token=?", token)
	if err != nil {
		return err
	}
	if err := s.recoverOwners(); err != nil {
		return err
	}
	s.removeWorkerInputs(token)
	return s.prune()
}

func (s *Store) recoverOwners() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE workers SET state='lost' WHERE state IN ('starting','running') AND lease<=(` + workerDBNow + `)`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT token,state FROM workers WHERE state IN ('running','lost')
		OR token IN (SELECT worker FROM runtime_slots)`)
	if err != nil {
		return err
	}
	type ownerState struct{ token, state string }
	var owners []ownerState
	var dead []string
	for rows.Next() {
		var owner ownerState
		if err := rows.Scan(&owner.token, &owner.state); err != nil {
			rows.Close()
			return err
		}
		owners = append(owners, owner)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if owner.state == "starting" {
			continue
		}
		if s.ownerAlive(owner.token) {
			continue
		}
		dead = append(dead, owner.token)
		if _, err := tx.Exec("UPDATE workers SET state='dead',lease=0 WHERE token=? AND state IN ('running','lost')", owner.token); err != nil {
			return err
		}
		// Slots are reclaimed only after the OS lock is released, not merely
		// a missed heartbeat: a paused process could still have a runtime call.
		if _, err := tx.Exec("DELETE FROM runtime_slots WHERE worker=?", owner.token); err != nil {
			return err
		}
	}
	if err := s.interruptUnowned(tx); err != nil {
		return err
	}
	// Keep launch/control results until the requesting process consumes them.
	// Abandoned controls must never be replayed after a crash.
	if _, err := tx.Exec(`UPDATE worker_commands SET state='done',response=?
		WHERE state!='done' AND worker IN (SELECT token FROM workers WHERE state NOT IN ('starting','running'))`,
		mustPayload(WorkerResponse{Error: "execution worker stopped before acknowledging control; outcome may be unknown"})); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM worker_commands WHERE deadline<?", time.Now().Add(-time.Hour).UnixNano()); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM execution_owners WHERE execution NOT IN (SELECT id FROM executions)"); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM workers WHERE state IN ('stopped','lost','dead')
		AND token NOT IN (SELECT worker FROM execution_owners)
		AND token NOT IN (SELECT worker FROM worker_commands)
		AND token NOT IN (SELECT worker FROM runtime_slots)`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, token := range dead {
		s.removeWorkerInputs(token)
	}
	return nil
}

// StageWorkerFiles keeps upload contents out of queue blobs (SQLite's blob
// limit is smaller than the allowed aggregate upload size). Only opaque
// indices cross the queue; clients never supply filesystem paths.
func (s *Store) StageWorkerFiles(token string, files []FileInput) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	dir := s.workerInputDir(token)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	names := make([]string, len(files))
	for i, file := range files {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d", i)), file.Data, 0o600); err != nil {
			s.removeWorkerInputs(token)
			return nil, err
		}
		names[i] = file.Name
	}
	return names, nil
}

func (s *Store) workerInputDir(token string) string {
	return filepath.Join(s.stateDir, "workers", token+".uploads")
}

func (s *Store) removeWorkerInputs(token string) {
	_ = os.RemoveAll(s.workerInputDir(token))
	_ = os.Remove(filepath.Join(s.stateDir, "workers", token+".config.json"))
}

func (s *Store) readWorkerFiles(names []string) ([]FileInput, error) {
	defer s.removeWorkerInputs(s.owner)
	files := make([]FileInput, len(names))
	for i, name := range names {
		data, err := os.ReadFile(filepath.Join(s.workerInputDir(s.owner), fmt.Sprintf("%d", i)))
		if err != nil {
			return nil, fmt.Errorf("reading staged worker upload: %w", err)
		}
		files[i] = FileInput{Name: name, Data: data}
	}
	return files, nil
}

func (s *Store) openOwnerLock(token string) (*os.File, error) {
	dir := filepath.Join(s.stateDir, "workers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, token+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
}

// ownerAlive probes the OS-held lock, not the PID, which may have been reused.
// Unreadable state is treated conservatively as a live owner.
func (s *Store) ownerAlive(token string) bool {
	if token == s.owner && s.ownerLock != nil {
		return true
	}
	f, err := s.openOwnerLock(token)
	if err != nil {
		return true // inaccessible state is not proof of process death
	}
	defer f.Close()
	return lockWorkerFile(f) != nil
}

// ConfigureRuntimeLimit sets the project-wide limit used by all owners.
func (s *Store) ConfigureRuntimeLimit(limit int) error {
	if limit < 1 {
		return fmt.Errorf("runtime concurrency limit must be positive")
	}
	_, err := s.db.Exec("INSERT INTO runtime_limit(id,slots) VALUES (1,?) ON CONFLICT(id) DO UPDATE SET slots=excluded.slots", limit)
	return err
}

// RecoverWorkers classifies expired owners without replaying any work.
func (s *Store) RecoverWorkers() error { return s.recoverOwners() }

// WorkerCommand queues a command for exactly one owner and waits for its
// acknowledgement. Timed-out or in-progress commands are never resubmitted.
func (s *Store) WorkerCommand(token string, req WorkerRequest) (WorkerResponse, error) {
	id := newExecID()
	deadline := time.Now().Add(20 * time.Second)
	result, err := s.db.Exec(`INSERT INTO worker_commands(id,worker,request,state,deadline)
		SELECT ?,?,?,'pending',? WHERE EXISTS
		(SELECT 1 FROM workers WHERE token=? AND state IN ('starting','running') AND lease>(`+workerDBNow+`))`,
		id, token, mustPayload(req), deadline.UnixNano(), token)
	if err != nil {
		return WorkerResponse{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return WorkerResponse{}, fmt.Errorf("execution worker is unavailable")
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state string
		var payload []byte
		err := s.db.QueryRow("SELECT state,response FROM worker_commands WHERE id=?", id).Scan(&state, &payload)
		if err != nil {
			return WorkerResponse{}, err
		}
		if state == "done" {
			var resp WorkerResponse
			if err := json.Unmarshal(payload, &resp); err != nil {
				return resp, fmt.Errorf("decoding worker %s response to %s command %s: %w", token, req.Op, id, err)
			}
			_, _ = s.db.Exec("DELETE FROM worker_commands WHERE id=?", id)
			return resp, responseError(resp)
		}
		if time.Now().After(deadline) {
			_, _ = s.db.Exec("UPDATE worker_commands SET state='expired' WHERE id=? AND state='pending'", id)
			return WorkerResponse{}, fmt.Errorf("worker acknowledgement timed out; control outcome is unknown (do not blindly retry)")
		}
		<-ticker.C
	}
}

func responseError(resp WorkerResponse) error {
	if resp.Changed != nil {
		return resp.Changed
	}
	if resp.Error != "" {
		if resp.Error == ErrChunksReadOnly.Error() {
			return ErrChunksReadOnly
		}
		return errors.New(resp.Error)
	}
	return nil
}

// PollWorker executes queued controls only once, while this store owns its
// lease. Execution writes are independently fenced in Save.
func (s *Store) PollWorker(eng *Engine) error {
	rows, err := s.db.Query(`SELECT id,request FROM worker_commands WHERE worker=? AND state='pending' AND deadline>?`,
		s.owner, time.Now().UnixNano())
	if err != nil {
		return err
	}
	type command struct {
		id      string
		payload []byte
	}
	var commands []command
	for rows.Next() {
		var c command
		if err := rows.Scan(&c.id, &c.payload); err != nil {
			rows.Close()
			return err
		}
		commands = append(commands, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range commands {
		result, err := s.db.Exec(`UPDATE worker_commands SET state='processing' WHERE id=? AND state='pending' AND deadline>(`+workerDBNow+`)
			AND EXISTS (SELECT 1 FROM workers WHERE token=? AND state='running' AND lease>(`+workerDBNow+`))`,
			c.id, s.owner)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			continue
		}
		var req WorkerRequest
		if err := json.Unmarshal(c.payload, &req); err != nil {
			return fmt.Errorf("decoding worker %s command %s: %w", s.owner, c.id, err)
		}
		resp := eng.ExecuteWorkerRequest(req)
		if resp.Error != "" {
			log.Printf("worker %s: %s command %s execution=%q run=%q pipeline=%q failed: %s",
				s.owner, req.Op, c.id, req.ExecID, req.RunID, req.Start.Pipeline, resp.Error)
		}
		if resp.ID != "" && resp.Error == "" {
			eng.mu.Lock()
			eng.workerStarted = true
			eng.mu.Unlock()
			log.Printf("worker %s owns execution %s", s.owner, resp.ID)
		}
		result, err = s.db.Exec(`UPDATE worker_commands SET state='done',response=? WHERE id=? AND state='processing'
			AND EXISTS (SELECT 1 FROM workers WHERE token=? AND state='running' AND lease>(`+workerDBNow+`))`,
			mustPayload(resp), c.id, s.owner)
		if err != nil {
			return err
		}
		n, _ = result.RowsAffected()
		if n != 1 {
			return fmt.Errorf("worker lost ownership before control acknowledgement")
		}
	}
	return nil
}

// ExecuteWorkerRequest applies an already-claimed private control message.
// The polling owner is responsible for lease checks and durable acknowledgement.
func (eng *Engine) ExecuteWorkerRequest(req WorkerRequest) WorkerResponse {
	var resp WorkerResponse
	var err error
	switch req.Op {
	case "start":
		if eng.WorkerHasStarted() {
			err = fmt.Errorf("worker already owns a top-level execution")
		} else {
			if len(req.Uploads) > 0 {
				req.Start.Files, err = eng.store.readWorkerFiles(req.Uploads)
			}
			if err == nil {
				resp.ID, err = eng.StartWith(req.Start)
			}
		}
	case "restart":
		if eng.WorkerHasStarted() {
			err = fmt.Errorf("worker already owns a top-level execution")
		} else {
			resp.ID, err = eng.Restart(req.ExecID, req.Restart)
		}
	case "approve":
		err = eng.Approve(req.ExecID, req.RunID, req.Decision, req.Actor, req.Text)
	case "edit":
		err = eng.SubmitManualEdit(req.ExecID, req.RunID, req.Text, req.Actor)
	case "retry":
		err = eng.ResolveFailure(req.ExecID, req.RunID, req.Decision)
	case "cancel":
		err = eng.Cancel(req.ExecID)
	case "delete":
		err = eng.Delete(req.ExecID)
	case "add-chunk":
		resp.Chunk, err = eng.AddChunk(req.ExecID, req.Text, req.Actor)
	case "complete-chunk":
		err = eng.CompleteChunk(req.ExecID, req.RunID, req.Actor)
	case "remove-chunk":
		err = eng.RemoveChunk(req.ExecID, req.RunID)
	default:
		err = fmt.Errorf("unknown internal worker operation %q", req.Op)
	}
	if err != nil {
		resp.Error = err.Error()
		errors.As(err, &resp.Changed)
		if errors.Is(err, ErrChunksReadOnly) {
			resp.Error = ErrChunksReadOnly.Error()
		}
	}
	if persistErr := eng.PersistenceError(); persistErr != nil {
		resp.Error = fmt.Sprintf("execution worker persistence failure: %v", persistErr)
	}
	return resp
}

// PersistenceError reports a fatal owned-write failure, never successful work.
func (eng *Engine) PersistenceError() error {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	return eng.persistError
}

// WorkerIdle is true only after a start has been acknowledged and all owned
// executions (including children and canceled calls winding down) have exited.
func (eng *Engine) WorkerIdle() bool {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	return eng.workerStarted && len(eng.execs) == 0
}

func (eng *Engine) WorkerHasStarted() bool {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	return eng.workerStarted
}

// SetWorkerLauncher isolates all future top-level starts (including restarts).
// Child pipelines deliberately bypass it through startLocked.
func (eng *Engine) SetWorkerLauncher(launch func(WorkerRequest) (string, error)) {
	eng.launchWorker = launch
}

// Store exposes the shared state connection to the process lifecycle owner.
func (eng *Engine) Store() *Store { return eng.store }

// AbortOwned stops all local work after the owner has been fenced.
func (eng *Engine) AbortOwned() {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if len(eng.execs) > 0 && eng.persistError == nil {
		eng.persistError = fmt.Errorf("execution owner stopped before work finished")
	}
	for _, execution := range eng.execs {
		execution.cancel()
	}
}

func (eng *Engine) remote(req WorkerRequest) (WorkerResponse, bool, error) {
	if eng.store == nil {
		return WorkerResponse{}, false, nil
	}
	var token, state string
	var lease int64
	err := eng.store.db.QueryRow(`SELECT w.token,w.state,w.lease FROM execution_owners o JOIN workers w ON w.token=o.worker
		WHERE o.execution=?`, req.ExecID).Scan(&token, &state, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkerResponse{}, false, nil
	}
	if err != nil {
		return WorkerResponse{}, true, err
	}
	if token == eng.store.owner {
		return WorkerResponse{}, false, nil
	}
	if state != "running" || lease <= time.Now().UnixNano() {
		// Dead owners cannot write again. Delete can now operate on terminal
		// history locally; all other controls fail explicitly.
		if err := eng.store.RecoverWorkers(); err != nil {
			return WorkerResponse{}, true, err
		}
		if req.Op == "delete" {
			return WorkerResponse{}, false, nil
		}
		if req.Op == "add-chunk" || req.Op == "complete-chunk" || req.Op == "remove-chunk" {
			return WorkerResponse{}, true, ErrChunksReadOnly
		}
		return WorkerResponse{}, true, fmt.Errorf("execution worker is no longer running")
	}
	resp, err := eng.store.WorkerCommand(token, req)
	return resp, true, err
}

func (s *Store) acquireSlot(ctx context.Context, limit int) (func() error, error) {
	id := newExecID()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		result, err := s.db.Exec(`INSERT INTO runtime_slots(id,worker)
			SELECT ?,? WHERE (SELECT COUNT(*) FROM runtime_slots) < COALESCE((SELECT slots FROM runtime_limit WHERE id=1),?)
			AND EXISTS (SELECT 1 FROM workers WHERE token=? AND state='running' AND lease>(`+workerDBNow+`))`,
			id, s.owner, limit, s.owner)
		if err != nil {
			return nil, err
		}
		n, _ := result.RowsAffected()
		if n == 1 {
			return func() error {
				_, err := s.db.Exec("DELETE FROM runtime_slots WHERE id=?", id)
				return err
			}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// persistedTree is used by read-only servers which have no local liveExecs.
// Caller holds eng.mu; every result is a fresh database snapshot.
func (eng *Engine) persistedTree(root string, includeRoot bool) []*Execution {
	if eng.store == nil {
		return nil
	}
	execution, err := eng.store.Get(root)
	if err != nil {
		return nil
	}
	var result []*Execution
	seen := map[string]bool{root: true}
	var visit func(*Execution)
	visit = func(parent *Execution) {
		var children []string
		for _, run := range parent.Runs {
			children = append(children, run.ChildExecutions...)
		}
		sort.Strings(children)
		for _, id := range children {
			if seen[id] {
				continue
			}
			seen[id] = true
			child, err := eng.store.Get(id)
			if err == nil && child.Parent != nil && child.Parent.Execution == parent.ID && !child.Status.Terminal() {
				result = append(result, child)
				visit(child)
			}
		}
	}
	if includeRoot {
		result = append(result, execution)
	}
	visit(execution)
	return result
}
