// Package cli implements the signalbox command line interface.
package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
	"github.com/kaladinstorm84/SignalBox/internal/server"
	"github.com/kaladinstorm84/SignalBox/internal/watcher"
)

// Version is stamped at build time via -ldflags.
var Version = "dev"

const usage = `SignalBox - YAML based AI agent workflow system with human approvals.

Usage:
  signalbox init [dir] [--force]     Create signalbox.yaml and a default pipeline
  signalbox validate                 Validate the config and all pipelines
  signalbox serve [--addr host:port] Serve the tracking & launch web UI
  signalbox run <pipeline> [--input text]  Run a pipeline in the terminal
  signalbox version                  Print the version
`

// Main is the entry point; returns the process exit code.
func Main(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return 2
	}
	var err error
	switch args[0] {
	case "init":
		err = cmdInit(args[1:])
	case "validate":
		err = cmdValidate(args[1:])
	case "serve":
		err = cmdServe(args[1:])
	case "run":
		err = cmdRun(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("signalbox %s\n", Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// --- init ---

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(abs, "pipelines"), 0o755); err != nil {
		return err
	}

	project := filepath.Base(abs)
	files := map[string]string{
		filepath.Join(abs, config.FileName):             fmt.Sprintf(configTemplate, project),
		filepath.Join(abs, "pipelines", "default.yaml"): defaultPipelineTemplate,
		filepath.Join(abs, ".gitignore"):                gitignoreTemplate,
	}
	for path, content := range files {
		if _, err := os.Stat(path); err == nil && !*force {
			fmt.Printf("skip   %s (exists, use --force to overwrite)\n", rel(abs, path))
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Printf("create %s\n", rel(abs, path))
	}
	fmt.Printf("\nProject %q initialised.\n", project)
	fmt.Println("Next steps:")
	fmt.Println("  signalbox validate        # check the config and pipelines")
	fmt.Println("  signalbox serve           # open the web UI")
	fmt.Println("  signalbox run default --input \"Hello SignalBox\"")
	return nil
}

func rel(base, path string) string {
	r, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return r
}

// --- validate ---

func cmdValidate(args []string) error {
	cfg, pipelines, err := loadProject()
	if err != nil {
		return err
	}
	if _, err := runtime.NewRegistry(cfg.Runtimes); err != nil {
		return err
	}
	fmt.Printf("config  %s: OK (%d runtimes)\n", config.FileName, len(cfg.Runtimes))
	for _, name := range pipeline.Names(pipelines) {
		p := pipelines[name]
		fmt.Printf("pipeline %q: OK (%d agents, entry %q)\n", p.Name, len(p.Nodes), p.Entry)
	}
	return nil
}

// --- serve ---

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "listen address (overrides signalbox.yaml)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, pipelines, err := loadProject()
	if err != nil {
		return err
	}
	eng, err := buildEngine(cfg, pipelines, true)
	if err != nil {
		return err
	}
	listen := cfg.Addr()
	if *addr != "" {
		listen = *addr
	}
	srv := server.New(eng, cfg.Project, func() (map[string]*pipeline.Pipeline, error) {
		_, pipelines, err := loadProject()
		return pipelines, err
	}, watcher.New(cfg.StatePath(), eng.Start), cfg.Watchers, cfg.Root)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return srv.ListenAndServe(ctx, listen)
}

// --- run ---

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	input := fs.String("input", "", "input handed to the entry agent (reads stdin if omitted)")
	autoApprove := fs.Bool("yes", false, "automatically approve all approval gates (non-interactive runs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: signalbox run <pipeline> [--input text] [--yes]")
	}
	pipelineName := fs.Arg(0)
	// The flag package stops at the first positional argument, so re-parse
	// anything after the pipeline name to support `run <pipeline> --input x`.
	if fs.NArg() > 1 {
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
	}

	cfg, pipelines, err := loadProject()
	if err != nil {
		return err
	}
	eng, err := buildEngine(cfg, pipelines, true)
	if err != nil {
		return err
	}

	in := *input
	if in == "" {
		fi, _ := os.Stdin.Stat()
		if (fi.Mode() & os.ModeCharDevice) == 0 {
			data, err := os.ReadFile("/dev/stdin")
			if err == nil {
				in = strings.TrimSpace(string(data))
			}
		}
	}
	if in == "" {
		return fmt.Errorf("no input: pass --input or pipe text on stdin")
	}

	execID, err := eng.Start(pipelineName, in)
	if err != nil {
		return err
	}
	fmt.Printf("execution %s started (pipeline %q)\n", execID, pipelineName)

	done, err := eng.Wait(execID)
	if err != nil {
		return err
	}

	reader := bufio.NewReader(os.Stdin)
	seen := map[string]engine.Status{}
	handled := map[string]bool{}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		printProgress(eng, execID, seen)
		for _, run := range eng.WaitingApprovals(execID) {
			if handled[run.ID] {
				continue
			}
			handled[run.ID] = true
			fmt.Printf("\n=== approval required: %s ===\n", run.NodeID)
			fmt.Printf("--- payload ---\n%s\n---------------\n", run.Input)
			approved := true
			comment := ""
			if *autoApprove {
				fmt.Println("auto-approved (--yes)")
			} else {
				fmt.Print("approve? [y/N]: ")
				line, _ := reader.ReadString('\n')
				approved = strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
				fmt.Print("comment (optional): ")
				c, _ := reader.ReadString('\n')
				comment = strings.TrimSpace(c)
			}
			if err := eng.Approve(execID, run.ID, approved, "terminal", comment); err != nil {
				fmt.Fprintf(os.Stderr, "approve failed: %v\n", err)
			}
		}
		for _, run := range eng.WaitingRetries(execID) {
			key := fmt.Sprintf("%s#%d", run.ID, run.Retries)
			if handled[key] {
				continue
			}
			handled[key] = true
			fmt.Printf("\n=== agent failed: %s ===\n%s\n", run.NodeID, run.Error)
			retry := false
			if *autoApprove {
				fmt.Println("not retrying (--yes)")
			} else {
				fmt.Print("retry? [y/N]: ")
				line, _ := reader.ReadString('\n')
				retry = strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
			}
			if err := eng.ResolveFailure(execID, run.ID, retry); err != nil {
				fmt.Fprintf(os.Stderr, "resolve failure: %v\n", err)
			}
		}
		select {
		case <-done:
			printProgress(eng, execID, seen)
			return printResult(eng, execID)
		case <-ticker.C:
		}
	}
}

func printProgress(eng *engine.Engine, execID string, seen map[string]engine.Status) {
	exec, err := eng.Get(execID)
	if err != nil {
		return
	}
	for _, r := range exec.Runs {
		if seen[r.ID] == r.Status {
			continue
		}
		seen[r.ID] = r.Status
		detail := ""
		if r.StageID != "" {
			detail = fmt.Sprintf(" (stage %s)", r.StageID)
		}
		if r.Error != "" {
			detail += ": " + r.Error
		}
		fmt.Printf("[%s] %-18s %s%s\n", time.Now().Format("15:04:05"), r.Status, r.NodeID, detail)
	}
}

func printResult(eng *engine.Engine, execID string) error {
	exec, err := eng.Get(execID)
	if err != nil {
		return err
	}
	fmt.Printf("\nexecution %s finished: %s\n", exec.ID, exec.Status)
	if exec.Error != "" {
		fmt.Printf("error: %s\n", exec.Error)
	}
	for _, f := range exec.FinalOutputs {
		fmt.Printf("\n=== result from %s ===\n%s\n", f.NodeID, f.Output)
	}
	if exec.Status != engine.StatusCompleted {
		return fmt.Errorf("execution did not complete")
	}
	return nil
}

// --- shared ---

func loadProject() (*config.Config, map[string]*pipeline.Pipeline, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return nil, nil, err
	}
	pipelines, errs := pipeline.LoadDir(cfg.PipelinesPath())
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, nil, fmt.Errorf("pipeline errors:\n  %s", strings.Join(msgs, "\n  "))
	}
	if len(pipelines) == 0 {
		return nil, nil, fmt.Errorf("no pipelines found in %s", cfg.PipelinesPath())
	}

	// Every runtime referenced by a pipeline must exist in the config.
	for _, p := range pipelines {
		for _, n := range p.Nodes {
			if n.Type == pipeline.TypeCode {
				if _, err := runtime.NewCode(&n, cfg.Root); err != nil {
					return nil, nil, err
				}
			}
			if n.Runtime == "" {
				continue
			}
			if _, ok := cfg.Runtimes[n.Runtime]; !ok {
				return nil, nil, fmt.Errorf("pipeline %q: agent %q references runtime %q which is not defined in %s", p.Name, n.ID, n.Runtime, config.FileName)
			}
		}
	}
	for _, watcher := range cfg.Watchers {
		if _, ok := pipelines[watcher.Pipeline]; !ok {
			return nil, nil, fmt.Errorf("watcher %q references unknown pipeline %q", watcher.Name, watcher.Pipeline)
		}
	}
	return cfg, pipelines, nil
}

func buildEngine(cfg *config.Config, pipelines map[string]*pipeline.Pipeline, persist bool) (*engine.Engine, error) {
	registry, err := runtime.NewRegistry(cfg.Runtimes)
	if err != nil {
		return nil, err
	}
	var store *engine.Store
	if persist {
		store, err = engine.NewStore(cfg.StatePath())
		if err != nil {
			return nil, err
		}
	}
	return engine.NewWithLimit(pipelines, registry, store, cfg.MaxConcurrentCalls, cfg.Root)
}
