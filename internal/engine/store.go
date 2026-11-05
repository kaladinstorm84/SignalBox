package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store persists executions as one JSON file each under
// <state_dir>/executions/. Writes are atomic (temp file + rename) so a crash
// never leaves a half-written record.
type Store struct {
	dir string
}

// NewStore creates the executions directory if needed.
func NewStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "executions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Save writes the execution record. Callers must hold the engine lock so the
// snapshot is consistent.
func (s *Store) Save(e *Execution) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(s.dir, e.ID+".json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Delete removes a persisted execution record. A missing file is not an error.
func (s *Store) Delete(id string) error {
	err := os.Remove(filepath.Join(s.dir, id+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadAll reads every persisted execution. Any execution that was still in
// flight (e.g. the server was stopped mid-run) is marked interrupted, along
// with its unfinished runs, so standard calls can be restarted from saved input.
func (s *Store) LoadAll() ([]*Execution, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []*Execution
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var e Execution
		if err := json.Unmarshal(data, &e); err != nil {
			// Skip corrupt records rather than refusing to start.
			continue
		}
		changed := false
		if !e.Status.Terminal() {
			e.Status = StatusInterrupted
			e.Error = "server stopped while the execution was in flight"
			changed = true
		}
		for _, r := range e.Runs {
			if !r.Status.Terminal() {
				r.Status = StatusInterrupted
				changed = true
			}
		}
		if changed {
			_ = s.Save(&e)
		}
		out = append(out, &e)
	}
	return out, nil
}
