package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const defaultExecutionRetention = 1000

// Store persists execution records in a SQLite database under the state dir.
type Store struct {
	db        *sql.DB
	stateDir  string
	retention int
}

// NewStore opens the execution database and imports any legacy JSON records.
// The optional retention limit defaults to 1000 terminal executions; zero
// disables automatic pruning.
func NewStore(stateDir string, retentionLimit ...int) (*Store, error) {
	if len(retentionLimit) > 1 {
		return nil, fmt.Errorf("at most one execution retention limit may be provided")
	}
	retention := defaultExecutionRetention
	if len(retentionLimit) > 0 {
		retention = retentionLimit[0]
	}
	if retention < 0 {
		return nil, fmt.Errorf("execution retention must not be negative")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(stateDir, "executions.db"))
	if err != nil {
		return nil, fmt.Errorf("opening execution database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, stateDir: stateDir, retention: retention}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.importLegacy(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("importing legacy execution history: %w", err)
	}
	if err := store.markInterrupted(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("recovering interrupted executions: %w", err)
	}
	if err := store.prune(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("applying execution retention: %w", err)
	}
	store.sweepUploads()
	return store, nil
}

// UploadsDir is the folder holding each execution's uploaded files.
func (s *Store) UploadsDir() string {
	return filepath.Join(s.stateDir, "uploads")
}

func (s *Store) removeUploads(ids []string) {
	for _, id := range ids {
		if id == "" || strings.ContainsAny(id, `/\.`) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.UploadsDir(), id))
	}
}

// sweepUploads removes upload folders whose execution no longer exists,
// for example after a crash between pruning and cleanup. Recently changed
// folders are left alone in case another process is starting an execution.
func (s *Store) sweepUploads() {
	entries, err := os.ReadDir(s.UploadsDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if info, err := entry.Info(); err != nil || time.Since(info.ModTime()) < 10*time.Minute {
			continue
		}
		var one int
		err := s.db.QueryRow("SELECT 1 FROM executions WHERE id = ?", entry.Name()).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			s.removeUploads([]string{entry.Name()})
		}
	}
}

func (s *Store) initialize() error {
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA busy_timeout = 5000",
		`CREATE TABLE IF NOT EXISTS executions (
			id TEXT PRIMARY KEY,
			pipeline TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			input TEXT NOT NULL,
			waiting_approvals INTEGER NOT NULL,
			waiting_manual_edits INTEGER NOT NULL,
			waiting_retries INTEGER NOT NULL,
			payload BLOB NOT NULL
		)`,
		"CREATE INDEX IF NOT EXISTS executions_created_at ON executions(created_at DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS executions_terminal_created_at ON executions(status, created_at DESC)",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("initializing execution database: %w", err)
		}
	}
	return nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Save writes the execution record atomically and applies the retention limit.
// Callers must hold the engine lock so the snapshot is consistent.
func (s *Store) Save(e *Execution) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	summary := summarizeExecution(e)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("starting execution save: %w", err)
	}
	defer tx.Rollback()
	if err := saveExecution(tx, e, payload, summary); err != nil {
		return err
	}
	var pruned []string
	if e.Status.Terminal() {
		if pruned, err = pruneExecutions(tx, s.retention); err != nil {
			return fmt.Errorf("pruning execution history: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving execution: %w", err)
	}
	s.removeUploads(pruned)
	return nil
}

func (s *Store) prune() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	pruned, err := pruneExecutions(tx, s.retention)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.removeUploads(pruned)
	return nil
}

func saveExecution(tx *sql.Tx, e *Execution, payload []byte, summary Summary) error {
	_, err := tx.Exec(`INSERT INTO executions
		(id, pipeline, status, created_at, input, waiting_approvals, waiting_manual_edits, waiting_retries, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		pipeline=excluded.pipeline, status=excluded.status, created_at=excluded.created_at,
		input=excluded.input, waiting_approvals=excluded.waiting_approvals,
		waiting_manual_edits=excluded.waiting_manual_edits, waiting_retries=excluded.waiting_retries,
		payload=excluded.payload`,
		e.ID, e.Pipeline, e.Status, e.CreatedAt.UTC().UnixNano(),
		summary.Input, summary.Waiting, summary.ManualEdits, summary.Retrying, payload)
	if err != nil {
		return fmt.Errorf("writing execution %q: %w", e.ID, err)
	}
	return nil
}

// Delete removes a persisted execution. A missing record is reported as an error.
func (s *Store) Delete(id string) error {
	result, err := s.db.Exec("DELETE FROM executions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting execution %q: %w", id, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking deleted execution %q: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("unknown execution %q", id)
	}
	s.removeUploads([]string{id})
	return nil
}

// Get loads one full execution record.
func (s *Store) Get(id string) (*Execution, error) {
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM executions WHERE id = ?", id).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("unknown execution %q", id)
		}
		return nil, fmt.Errorf("loading execution %q: %w", id, err)
	}
	var execution Execution
	if err := json.Unmarshal(payload, &execution); err != nil {
		return nil, fmt.Errorf("decoding execution %q: %w", id, err)
	}
	return &execution, nil
}

// ListPage returns one newest-first page and the total matching record count.
func (s *Store) ListPage(page, pageSize int) ([]Summary, int, error) {
	if page < 1 || pageSize < 1 {
		return nil, 0, fmt.Errorf("page and page size must be positive")
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM executions").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting executions: %w", err)
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		return []Summary{}, total, nil
	}
	rows, err := s.db.Query(`SELECT id, pipeline, status, created_at, input,
		waiting_approvals, waiting_manual_edits, waiting_retries
		FROM executions ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("listing executions: %w", err)
	}
	defer rows.Close()
	items := make([]Summary, 0)
	for rows.Next() {
		var item Summary
		var created int64
		if err := rows.Scan(&item.ID, &item.Pipeline, &item.Status, &created, &item.Input,
			&item.Waiting, &item.ManualEdits, &item.Retrying); err != nil {
			return nil, 0, fmt.Errorf("reading execution summary: %w", err)
		}
		item.CreatedAt = time.Unix(0, created).UTC()
		item.Input = firstChars(item.Input, 160)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading execution summaries: %w", err)
	}
	return items, total, nil
}

// LoadAll returns all records, primarily for migration and tests.
func (s *Store) LoadAll() ([]*Execution, error) {
	rows, err := s.db.Query("SELECT payload FROM executions ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, fmt.Errorf("loading execution history: %w", err)
	}
	defer rows.Close()
	var out []*Execution
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("reading execution history: %w", err)
		}
		var execution Execution
		if err := json.Unmarshal(payload, &execution); err != nil {
			return nil, fmt.Errorf("decoding execution history: %w", err)
		}
		out = append(out, &execution)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading execution history: %w", err)
	}
	return out, nil
}

func (s *Store) importLegacy() error {
	dir := filepath.Join(s.stateDir, "executions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	var imported []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		var execution Execution
		if err := json.Unmarshal(data, &execution); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		interruptExecution(&execution)
		payload, err := json.Marshal(&execution)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := saveExecution(tx, &execution, payload, summarizeExecution(&execution)); err != nil {
			_ = tx.Rollback()
			return err
		}
		imported = append(imported, path)
	}
	if _, err := pruneExecutions(tx, s.retention); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, path := range imported {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing imported file %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) markInterrupted() error {
	rows, err := s.db.Query(`SELECT payload FROM executions
		WHERE status IN ('pending', 'running', 'waiting_approval', 'waiting_manual_edit',
			'waiting_inputs', 'waiting_retry', 'canceled')`)
	if err != nil {
		return err
	}
	var changed []*Execution
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			_ = rows.Close()
			return err
		}
		var execution Execution
		if err := json.Unmarshal(payload, &execution); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decoding in-flight execution: %w", err)
		}
		if interruptExecution(&execution) {
			changed = append(changed, &execution)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, execution := range changed {
		if err := s.Save(execution); err != nil {
			return err
		}
	}
	return nil
}

func interruptExecution(e *Execution) bool {
	changed := false
	if !e.Status.Terminal() {
		e.Status = StatusInterrupted
		e.Error = "server stopped while the execution was in flight"
		changed = true
	}
	for _, run := range e.Runs {
		if !run.Status.Terminal() {
			run.Status = StatusInterrupted
			changed = true
		}
	}
	return changed
}

// pruneExecutions deletes terminal executions beyond the retention limit and
// returns their IDs so the caller can remove their uploads after commit.
func pruneExecutions(tx *sql.Tx, retention int) ([]string, error) {
	if retention == 0 {
		return nil, nil
	}
	rows, err := tx.Query(`DELETE FROM executions
		WHERE status IN ('completed', 'failed', 'rejected', 'canceled', 'interrupted')
		AND id NOT IN (
			SELECT id FROM executions
			WHERE status IN ('completed', 'failed', 'rejected', 'canceled', 'interrupted')
			ORDER BY created_at DESC, id DESC LIMIT ?
		) RETURNING id`, retention)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func summarizeExecution(e *Execution) Summary {
	item := Summary{
		ID:         e.ID,
		Pipeline:   e.Pipeline,
		Input:      firstChars(e.Input, 160),
		Status:     e.Status,
		CreatedAt:  e.CreatedAt,
		FinishedAt: e.FinishedAt,
	}
	if e.Parent != nil {
		item.Parent = e.Parent.Execution
	}
	for _, run := range e.Runs {
		switch run.Status {
		case StatusWaitingApproval:
			item.Waiting++
		case StatusWaitingManualEdit:
			item.ManualEdits++
		case StatusWaitingRetry:
			item.Retrying++
		}
	}
	return item
}
