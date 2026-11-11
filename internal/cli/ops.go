package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/kaladinstorm84/SignalBox/internal/engine"
)

const opsUsage = `Execution commands (talk to a running signalbox serve):
  signalbox list [--page N] [--page-size N]
  signalbox show <execution> [--json]
  signalbox start <pipeline> [--input text] [--param k=v]... [--file path]...
  signalbox approve <execution> [--run id] [--comment text] [--by name]
  signalbox reject <execution> [--run id] [--comment text] [--by name]
  signalbox edit <execution> [--run id] (--output text | --file path) [--by name]
  signalbox retry <execution> [--run id]
  signalbox restart <execution> [--run id | --node id] [--stage id] [--force]
  signalbox rerun <execution>
  signalbox cancel <execution>
  signalbox delete <execution>

Common flags:
  --addr host:port   SignalBox server (default: server in signalbox.yaml)

approve, reject, edit, and retry pick the waiting run automatically when
exactly one run is waiting. restart without --run or --node restarts from the
most recent failed, interrupted, or canceled run.
`

// opsArgs is a parsed command line: positional arguments, flag values
// (repeatable flags keep every value), and boolean switches.
type opsArgs struct {
	positional []string
	values     map[string][]string
	switches   map[string]bool
}

func (a opsArgs) value(name string) string {
	if v := a.values[name]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

// parseOpsArgs accepts flags anywhere on the line, as --name value,
// --name=value, or (for switches) --name.
func parseOpsArgs(args []string, valueFlags, switchFlags []string) (opsArgs, error) {
	parsed := opsArgs{values: map[string][]string{}, switches: map[string]bool{}}
	isValue := map[string]bool{"addr": true}
	for _, f := range valueFlags {
		isValue[f] = true
	}
	isSwitch := map[string]bool{}
	for _, f := range switchFlags {
		isSwitch[f] = true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			parsed.positional = append(parsed.positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		switch {
		case isSwitch[name]:
			parsed.switches[name] = !hasValue || value == "true"
		case isValue[name]:
			if !hasValue {
				if i+1 >= len(args) {
					return parsed, fmt.Errorf("flag --%s needs a value", name)
				}
				i++
				value = args[i]
			}
			parsed.values[name] = append(parsed.values[name], value)
		default:
			return parsed, fmt.Errorf("unknown flag %s\n\n%s", arg, opsUsage)
		}
	}
	return parsed, nil
}

func opsBase(a opsArgs) (string, error) {
	addr := a.value("addr")
	if addr == "" {
		var err error
		if addr, err = configuredServerAddr(); err != nil {
			return "", fmt.Errorf("%w (or pass --addr host:port)", err)
		}
	}
	return "http://" + addr + "/api", nil
}

func cliUser() string {
	for _, name := range []string{"USER", "USERNAME"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return "cli"
}

// cmdOps runs one execution-management command against the server API, so
// the CLI and the web UI share the same behaviour.
func cmdOps(command string, args []string, out io.Writer) error {
	valueFlags := map[string][]string{
		"list":    {"page", "page-size"},
		"show":    nil,
		"start":   {"input", "param", "file"},
		"approve": {"run", "comment", "by"},
		"reject":  {"run", "comment", "by"},
		"edit":    {"run", "output", "file", "by"},
		"retry":   {"run"},
		"restart": {"run", "node", "stage"},
		"rerun":   nil,
		"cancel":  nil,
		"delete":  nil,
	}
	switchFlags := map[string][]string{"show": {"json"}, "restart": {"force"}}
	a, err := parseOpsArgs(args, valueFlags[command], switchFlags[command])
	if err != nil {
		return err
	}
	wantArgs := 1
	if command == "list" {
		wantArgs = 0
	}
	if len(a.positional) != wantArgs {
		return fmt.Errorf("signalbox %s: expected %d argument(s), got %d\n\n%s", command, wantArgs, len(a.positional), opsUsage)
	}
	base, err := opsBase(a)
	if err != nil {
		return err
	}
	if command == "list" {
		return opsList(base, a, out)
	}
	if command == "start" {
		return opsStart(base, a, out)
	}

	id := a.positional[0]
	execURL := base + "/executions/" + url.PathEscape(id)
	switch command {
	case "show":
		return opsShow(execURL, a.switches["json"], out)
	case "approve", "reject":
		runID, err := pickWaitingRun(execURL, a.value("run"), engine.StatusWaitingApproval)
		if err != nil {
			return err
		}
		by := a.value("by")
		if by == "" {
			by = cliUser()
		}
		body := map[string]any{"run_id": runID, "approved": command == "approve", "approved_by": by, "comment": a.value("comment")}
		if err := apiRequest(http.MethodPost, execURL+"/approve", body, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %sd by %s\n", runID, command, by)
	case "edit":
		output := a.value("output")
		if file := a.value("file"); file != "" {
			if output != "" {
				return fmt.Errorf("pass either --output or --file, not both")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			output = string(data)
		}
		if output == "" {
			return fmt.Errorf("signalbox edit needs --output text or --file path")
		}
		runID, err := pickWaitingRun(execURL, a.value("run"), engine.StatusWaitingManualEdit)
		if err != nil {
			return err
		}
		by := a.value("by")
		if by == "" {
			by = cliUser()
		}
		if err := apiRequest(http.MethodPost, execURL+"/manual-edit", map[string]string{"run_id": runID, "output": output, "edited_by": by}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s edited by %s\n", runID, by)
	case "retry":
		runID, err := pickWaitingRun(execURL, a.value("run"), engine.StatusWaitingRetry)
		if err != nil {
			return err
		}
		if err := apiRequest(http.MethodPost, execURL+"/retry", map[string]string{"run_id": runID}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s retrying\n", runID)
	case "restart":
		if a.value("run") != "" && a.value("node") != "" {
			return fmt.Errorf("pass either --run or --node, not both")
		}
		body := map[string]any{"run_id": a.value("run"), "node_id": a.value("node"), "stage": a.value("stage"), "force": a.switches["force"]}
		var started struct {
			ID string `json:"id"`
		}
		if err := apiRequest(http.MethodPost, execURL+"/restart", body, &started); err != nil {
			if strings.Contains(err.Error(), "has changed since execution") {
				return fmt.Errorf("%w\nrerun with --force to restart on the current definition", err)
			}
			return err
		}
		fmt.Fprintf(out, "started %s (restart of %s)\n", started.ID, id)
	case "rerun":
		var started struct {
			ID string `json:"id"`
		}
		if err := apiRequest(http.MethodPost, execURL+"/rerun", map[string]string{}, &started); err != nil {
			return err
		}
		fmt.Fprintf(out, "started %s (rerun of %s)\n", started.ID, id)
	case "cancel":
		if err := apiRequest(http.MethodPost, execURL+"/cancel", map[string]string{}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s canceled\n", id)
	case "delete":
		if err := apiRequest(http.MethodDelete, execURL, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s deleted\n", id)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	return nil
}

func opsList(base string, a opsArgs, out io.Writer) error {
	q := url.Values{}
	if v := a.value("page"); v != "" {
		q.Set("page", v)
	}
	if v := a.value("page-size"); v != "" {
		q.Set("page_size", v)
	}
	target := base + "/executions"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var page struct {
		Executions []engine.Summary `json:"executions"`
		Page       int              `json:"page"`
		TotalPages int              `json:"total_pages"`
		Total      int              `json:"total"`
	}
	if err := apiRequest(http.MethodGet, target, nil, &page); err != nil {
		return err
	}
	if len(page.Executions) == 0 {
		fmt.Fprintln(out, "no executions")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPIPELINE\tSTATUS\tCREATED\tINPUT")
	for _, e := range page.Executions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.ID, e.Pipeline, e.Status, e.CreatedAt.Local().Format("2006-01-02 15:04"), oneLine(e.Input, 50))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if page.TotalPages > 1 {
		fmt.Fprintf(out, "page %d of %d (%d executions)\n", page.Page, page.TotalPages, page.Total)
	}
	return nil
}

func opsStart(base string, a opsArgs, out io.Writer) error {
	params := map[string]string{}
	for _, kv := range a.values["param"] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("--param must be key=value, got %q", kv)
		}
		params[k] = v
	}
	var started struct {
		ID string `json:"id"`
	}
	if paths := a.values["file"]; len(paths) > 0 {
		if err := startMultipart(base+"/executions", a.positional[0], a.value("input"), params, paths, &started); err != nil {
			return err
		}
		fmt.Fprintf(out, "started %s (%d file(s) attached)\n", started.ID, len(paths))
		return nil
	}
	body := map[string]any{"pipeline": a.positional[0], "input": a.value("input"), "params": params}
	if err := apiRequest(http.MethodPost, base+"/executions", body, &started); err != nil {
		return err
	}
	fmt.Fprintf(out, "started %s\n", started.ID)
	return nil
}

func getExecution(execURL string) (*engine.Execution, error) {
	var exec engine.Execution
	if err := apiRequest(http.MethodGet, execURL, nil, &exec); err != nil {
		return nil, err
	}
	return &exec, nil
}

func opsShow(execURL string, asJSON bool, out io.Writer) error {
	if asJSON {
		var raw json.RawMessage
		if err := apiRequest(http.MethodGet, execURL, nil, &raw); err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return enc.Encode(v)
	}
	exec, err := getExecution(execURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Execution %s\nPipeline:  %s\nStatus:    %s\nCreated:   %s\n", exec.ID, exec.Pipeline, exec.Status, exec.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	if exec.Error != "" {
		fmt.Fprintf(out, "Error:     %s\n", exec.Error)
	}
	if p := exec.Parent; p != nil {
		fmt.Fprintf(out, "Part of:   %s (pipeline node %s, run %s)\n", p.Execution, p.NodeID, p.RunID)
	}
	if s := exec.Section; s != nil {
		from, until := s.From, s.Until
		if from == "" {
			from = "entry"
		}
		if until == "" {
			until = "end"
		}
		fmt.Fprintf(out, "Section:   %s .. %s\n", from, until)
	}
	if r := exec.Recovery; r != nil {
		from := r.NodeID
		if r.Stage != "" {
			from += " stage " + r.Stage
		}
		fmt.Fprintf(out, "Restarted: from %s run %s (%s), %d run(s) reused", r.SourceExecution, r.SourceRun, from, r.ReusedRuns)
		if r.Forced {
			fmt.Fprint(out, ", forced")
		}
		fmt.Fprintln(out)
		for _, warning := range r.Warnings {
			fmt.Fprintf(out, "  warning: %s\n", warning)
		}
	}
	fmt.Fprintln(out)
	for _, f := range exec.Files {
		note := ""
		if f.ReusedFrom != "" {
			note = ", reused from " + f.ReusedFrom
		}
		fmt.Fprintf(out, "File:      %s (%d bytes, %s%s)\n", f.Name, f.Size, f.ContentType, note)
	}
	if len(exec.Files) > 0 {
		fmt.Fprintln(out)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RUN\tNODE\tTYPE\tSTATUS\tNOTE")
	for _, run := range exec.Runs {
		node := run.NodeID
		if run.StageID != "" {
			node = "  " + node + " [" + run.StageID + "]"
		}
		var notes []string
		if run.ReusedFrom != "" {
			notes = append(notes, "reused from "+run.ReusedFrom)
		}
		if n := len(run.ChildExecutions); n > 0 {
			notes = append(notes, "child "+run.ChildExecutions[n-1])
		}
		if run.Files != nil {
			notes = append(notes, fmt.Sprintf("files %s: %d", run.Files.Mode, len(run.Files.Names)))
		}
		if run.Error != "" {
			notes = append(notes, oneLine(run.Error, 60))
		}
		for _, stage := range run.Stages {
			notes = append(notes, stage.StageID+":"+string(stage.Status))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", run.ID, node, run.Type, run.Status, strings.Join(notes, "; "))
	}
	for _, s := range exec.Surfaced {
		fmt.Fprintf(w, "%s\t  %s\t%s\t%s\t%s\n", s.Run.ID, s.Run.NodeID, s.Run.Type, s.Run.Status, "in child "+s.Execution+" ("+s.Pipeline+")")
	}
	return w.Flush()
}

// pickWaitingRun returns the explicit run ID, or the only matching wait among
// this execution and its surfaced descendants. Explicit IDs are validated by
// the server when the command is submitted.
func pickWaitingRun(execURL, runID string, want engine.Status) (string, error) {
	if runID != "" {
		return runID, nil
	}
	exec, err := getExecution(execURL)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, run := range exec.Runs {
		if run.Status == want {
			matches = append(matches, run.ID+" ("+run.NodeID+")")
		}
	}
	for _, s := range exec.Surfaced {
		if s.Run.Status == want {
			matches = append(matches, s.Run.ID+" ("+s.Run.NodeID+" in "+s.Execution+")")
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("execution %s has no run in %s", exec.ID, want)
	case 1:
		id, _, _ := strings.Cut(matches[0], " ")
		return id, nil
	}
	return "", fmt.Errorf("execution %s has %d runs in %s; pick one with --run: %s", exec.ID, len(matches), want, strings.Join(matches, ", "))
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}
