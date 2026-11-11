// Package watcher turns changed command output into pipeline launches and
// persists output hashes to avoid replaying the same poll after a restart.
package watcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

// StartPipeline returns the execution ID only after a launch is accepted.
type StartPipeline func(name, input string) (string, error)
type pollFunc func(context.Context, config.WatcherConfig) (string, error)

// Runner shares launch and polling dependencies; each Run owns one poll loop.
type Runner struct {
	stateDir string
	start    StartPipeline
	poll     pollFunc
}

type savedState struct {
	OutputHash string `json:"output_hash"`
}

func New(stateDir string, start StartPipeline) *Runner {
	return &Runner{stateDir: stateDir, start: start, poll: pollCommand}
}

// Run polls immediately, then repeats until ctx is canceled.
func (r *Runner) Run(ctx context.Context, cfg config.WatcherConfig) {
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil || interval <= 0 {
		log.Printf("watcher %q: invalid interval %q", cfg.Name, cfg.Interval)
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.pollOnce(ctx, cfg); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("watcher %q: %v", cfg.Name, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pollOnce saves the new hash only after any required launch succeeds.
// Empty output updates the hash too, allowing a later non-empty repeat to fire.
func (r *Runner) pollOnce(ctx context.Context, cfg config.WatcherConfig) error {
	output, err := r.poll(ctx, cfg)
	if err != nil {
		return fmt.Errorf("poll command failed: %w", err)
	}
	output = strings.TrimSpace(output)
	hash := sha256.Sum256([]byte(output))
	statePath := r.statePath(cfg.Name)
	var previous savedState
	data, err := os.ReadFile(statePath)
	if err == nil {
		if err := json.Unmarshal(data, &previous); err != nil {
			return fmt.Errorf("read state: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read state: %w", err)
	}

	outputHash := hex.EncodeToString(hash[:])
	if previous.OutputHash == outputHash {
		return nil
	}
	if output != "" {
		id, err := r.start(cfg.Pipeline, output)
		if err != nil {
			return fmt.Errorf("start pipeline %q: %w", cfg.Pipeline, err)
		}
		log.Printf("watcher %q started execution %s (pipeline %q)", cfg.Name, id, cfg.Pipeline)
	}
	if err := writeState(statePath, savedState{OutputHash: outputHash}); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

func (r *Runner) statePath(name string) string {
	nameHash := sha256.Sum256([]byte(name))
	return filepath.Join(r.stateDir, "watchers", hex.EncodeToString(nameHash[:])+".json")
}

func writeState(path string, state savedState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".watcher-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := json.NewEncoder(file).Encode(state); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err == nil {
		return nil
	} else if _, statErr := os.Stat(path); statErr == nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return removeErr
		}
		return os.Rename(tempPath, path)
	} else {
		return err
	}
}

func pollCommand(ctx context.Context, cfg config.WatcherConfig) (string, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("command timed out after %s", timeout)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(string(output))
		}
		if detail != "" {
			return "", fmt.Errorf("%w: %s", err, detail)
		}
		return "", err
	}
	return string(output), nil
}
