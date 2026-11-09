package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
)

const workerTokenEnv = "SIGNALBOX_INTERNAL_WORKER_TOKEN"
const workerStateEnv = "SIGNALBOX_INTERNAL_WORKER_STATE"

func launchWorker(cfg *config.Config, store *engine.Store, req engine.WorkerRequest) (string, error) {
	token, err := store.PrepareWorker()
	if err != nil {
		return "", fmt.Errorf("preparing execution worker: %w", err)
	}
	started := false
	defer func() {
		if !started {
			_ = store.FinishWorker(token)
		}
	}()
	req.Uploads, err = store.StageWorkerFiles(token, req.Start.Files)
	if err != nil {
		return "", fmt.Errorf("staging worker uploads: %w", err)
	}
	req.Start.Files = nil
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	logDir := filepath.Join(cfg.StatePath(), "workers")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", err
	}
	cfgData, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(logDir, token+".config.json"), cfgData, 0o600); err != nil {
		return "", fmt.Errorf("staging worker configuration: %w", err)
	}
	logPath := filepath.Join(logDir, token+".log")
	output, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer output.Close()
	cmd := exec.Command(executable, "_worker")
	cmd.Dir = cfg.Root
	cmd.Env = append(os.Environ(), workerTokenEnv+"="+token, workerStateEnv+"="+cfg.StatePath())
	cmd.Stdout, cmd.Stderr = output, output
	detachWorker(cmd)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting detached worker (host must permit job breakaway on Windows): %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if err != nil {
			log.Printf("execution worker pid=%d exited with error (log %s): %v", cmd.Process.Pid, logPath, err)
		}
		exited <- err
	}()
	type result struct {
		response engine.WorkerResponse
		err      error
	}
	ack := make(chan result, 1)
	go func() {
		response, err := store.WorkerCommand(token, req)
		ack <- result{response, err}
	}()
	select {
	case r := <-ack:
		if r.err == nil && r.response.ID != "" {
			started = true
			log.Printf("execution %s: detached worker pid=%d log=%s", r.response.ID, cmd.Process.Pid, logPath)
			return r.response.ID, nil
		}
		_ = cmd.Process.Kill()
		<-exited
		if r.err == nil {
			r.err = fmt.Errorf("worker acknowledged launch without an execution ID")
		}
		return "", fmt.Errorf("execution worker launch failed (log %s): %w", logPath, r.err)
	case err := <-exited:
		// A fast execution can finish and exit just after acknowledging. Wait
		// for its durable result before interpreting the exit as launch failure.
		_ = store.FinishWorker(token)
		r := <-ack
		if err == nil && r.err == nil && r.response.ID != "" {
			started = true
			log.Printf("execution %s: detached worker pid=%d log=%s", r.response.ID, cmd.Process.Pid, logPath)
			return r.response.ID, nil
		}
		return "", fmt.Errorf("execution worker exited during launch (log %s): exit=%v; acknowledgement=%v", logPath, err, r.err)
	}
}

// cmdWorker is intentionally absent from help. It accepts no commands/paths
// from arguments and requires a one-shot launch capability in shared state.
// No network listener or second UI is started.
func cmdWorker(args []string) error {
	token := os.Getenv(workerTokenEnv)
	state := os.Getenv(workerStateEnv)
	_ = os.Unsetenv(workerTokenEnv)
	_ = os.Unsetenv(workerStateEnv)
	if len(args) != 0 || len(token) != 64 || strings.Trim(token, "0123456789abcdef") != "" || state == "" {
		return fmt.Errorf("_worker is an internal command requiring a server launch")
	}
	path := filepath.Join(state, "workers", token+".config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading worker launch configuration: %w", err)
	}
	_ = os.Remove(path)
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("decoding worker launch configuration: %w", err)
	}
	if filepath.Clean(cfg.StatePath()) != filepath.Clean(state) {
		return fmt.Errorf("worker launch state directory mismatch")
	}
	pipelines, err := loadProjectPipelines(&cfg)
	if err != nil {
		return err
	}
	eng, err := buildEngine(&cfg, pipelines, true)
	if err != nil {
		return err
	}
	store := eng.Store()
	defer store.Close()
	if err := store.ClaimWorker(token, os.Getpid()); err != nil {
		return err
	}
	defer store.FinishWorker(token)
	defer eng.AbortOwned()
	ownerCtx, stopOwner := context.WithCancel(context.Background())
	ownerErrors, ownerDone := maintainOwner(ownerCtx, store)
	defer func() {
		stopOwner()
		<-ownerDone
	}()
	log.Printf("worker %s pid=%d started", token, os.Getpid())
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	startDeadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-ownerErrors:
			return fmt.Errorf("worker lost ownership: %w", err)
		case <-ticker.C:
			if err := store.PollWorker(eng); err != nil {
				return fmt.Errorf("worker control failed: %w", err)
			}
			if err := eng.PersistenceError(); err != nil {
				return fmt.Errorf("worker persistence failed: %w", err)
			}
			if eng.WorkerIdle() {
				log.Printf("worker %s finished", token)
				return nil
			}
			if time.Now().After(startDeadline) && !eng.WorkerHasStarted() {
				return fmt.Errorf("worker did not receive a successful start")
			}
		}
	}
}

func recoverWorkers(ctx context.Context, store *engine.Store) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := store.RecoverWorkers(); err != nil {
				log.Printf("recovering execution workers: %v", err)
			}
		}
	}
}

func renewForeground(ctx context.Context, eng *engine.Engine) {
	failures, done := maintainOwner(ctx, eng.Store())
	defer func() { <-done }()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-failures:
			log.Printf("foreground execution lost ownership: %v", err)
			eng.AbortOwned()
			return
		case <-ticker.C:
			if err := eng.Store().PollWorker(eng); err != nil {
				log.Printf("foreground execution control failed: %v", err)
				eng.AbortOwned()
				return
			}
		}
	}
}

func maintainOwner(ctx context.Context, store *engine.Store) (<-chan error, <-chan struct{}) {
	failures := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := store.RenewWorker(); err != nil {
					failures <- err
					return
				}
				if err := store.RecoverWorkers(); err != nil {
					log.Printf("recovering execution owners: %v", err)
				}
			}
		}
	}()
	return failures, done
}
