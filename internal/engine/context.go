package engine

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// contextSourceType reports whether completed runs of a node type produce
// output worth offering as context. Approval and llm_gate runs pass their
// input through unchanged, so they would only duplicate other entries.
func contextSourceType(t pipeline.NodeType) bool {
	switch t {
	case pipeline.TypeStandard, pipeline.TypeCode, pipeline.TypeOpenSpec, pipeline.TypeSession, pipeline.TypeOrchestrator, pipeline.TypeManualEdit:
		return true
	}
	return false
}

// selectContext deterministically assembles the context for run from the
// execution's earlier runs. It does not modify runs.
func selectContext(cfg *pipeline.ContextConfig, runs []*NodeRun, run *NodeRun) *ContextSelection {
	sel := &ContextSelection{Strategy: cfg.Strategy, Window: cfg.Window, MaxChars: cfg.MaxChars}
	if cfg.Strategy == pipeline.ContextNone {
		return sel
	}

	var candidates []*NodeRun
	for _, r := range runs {
		if r == nil || r.ID == run.ID || r.Status != StatusCompleted || r.Output == "" || r.FinishedAt == nil || !contextSourceType(r.Type) {
			continue
		}
		if cfg.Strategy == pipeline.ContextOwn && r.NodeID != run.NodeID {
			continue
		}
		candidates = append(candidates, r)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].FinishedAt.Before(*candidates[j].FinishedAt)
	})

	// Outputs the node already receives verbatim (its handoff payload,
	// joined inputs, or additional inputs) are not repeated.
	available := candidates[:0:0]
	for _, r := range candidates {
		if strings.Contains(run.Input, r.Output) {
			sel.Skipped++
			continue
		}
		available = append(available, r)
	}
	sel.Available = len(available)

	if cfg.Window > 0 && len(available) > cfg.Window {
		available = available[len(available)-cfg.Window:]
	}

	// Apply the character budget newest-first, so the most recent work is
	// kept whole and only the oldest included entry may be truncated.
	type picked struct {
		r     *NodeRun
		text  string
		entry ContextEntry
	}
	var chosen []picked
	remaining := cfg.MaxChars
	for i := len(available) - 1; i >= 0; i-- {
		r := available[i]
		n := utf8.RuneCountInString(r.Output)
		p := picked{r: r, text: r.Output, entry: ContextEntry{RunID: r.ID, NodeID: r.NodeID, Chars: n, OriginalChars: n}}
		if cfg.MaxChars > 0 {
			if remaining <= 0 {
				break
			}
			if n > remaining {
				p.text = truncateRunes(r.Output, remaining)
				p.entry.Chars = remaining
				p.entry.Truncated = true
			}
			remaining -= p.entry.Chars
		}
		chosen = append(chosen, p)
	}

	var b strings.Builder
	if len(chosen) > 0 {
		fmt.Fprintf(&b, "--- context (%s): %d of %d earlier outputs ---", cfg.Strategy, len(chosen), sel.Available)
	}
	for i := len(chosen) - 1; i >= 0; i-- {
		p := chosen[i]
		sel.Entries = append(sel.Entries, p.entry)
		sel.Chars += p.entry.Chars
		b.WriteString("\n\n")
		if p.entry.Truncated {
			fmt.Fprintf(&b, "--- context from %s (truncated to %d of %d characters) ---\n%s\n[... %d characters truncated]",
				p.r.NodeID, p.entry.Chars, p.entry.OriginalChars, p.text, p.entry.OriginalChars-p.entry.Chars)
		} else {
			fmt.Fprintf(&b, "--- context from %s ---\n%s", p.r.NodeID, p.text)
		}
	}
	sel.Text = b.String()
	return sel
}

func truncateRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// runtimeInput is the text sent to a runtime: the run's input followed by
// any selected context. Run.Input itself never includes context, so gates
// and approvals keep passing their original payload downstream.
func runtimeInput(run *NodeRun) string {
	if run.Context == nil || run.Context.Text == "" {
		return run.Input
	}
	if run.Input == "" {
		return run.Context.Text
	}
	return run.Input + "\n\n" + run.Context.Text
}
