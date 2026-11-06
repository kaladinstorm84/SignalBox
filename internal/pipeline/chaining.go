package pipeline

import (
	"fmt"
	"sort"
	"strings"
)

// CheckReferences validates pipeline nodes against the whole set of loaded
// pipelines: each referenced pipeline must exist, from/until must select a
// runnable section of it, params must be declared by it, and references
// must not form a cycle. Errors are returned in a stable order.
func CheckReferences(pipelines map[string]*Pipeline) []error {
	var errs []error
	for _, name := range Names(pipelines) {
		p := pipelines[name]
		for i := range p.Nodes {
			n := &p.Nodes[i]
			if n.Type != TypePipeline {
				continue
			}
			if err := checkReference(n, pipelines); err != nil {
				errs = append(errs, fmt.Errorf("pipeline %q: agent %q: %w", p.Name, n.ID, err))
			}
		}
	}
	if err := checkReferenceCycles(pipelines); err != nil {
		errs = append(errs, err)
	}
	return errs
}

// References returns the names of pipelines that p's pipeline nodes run.
func (p *Pipeline) References() []string {
	seen := map[string]bool{}
	var names []string
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.Type == TypePipeline && !seen[n.Pipeline] {
			seen[n.Pipeline] = true
			names = append(names, n.Pipeline)
		}
	}
	sort.Strings(names)
	return names
}

func checkReference(n *Node, pipelines map[string]*Pipeline) error {
	child, ok := pipelines[n.Pipeline]
	if !ok {
		return fmt.Errorf("runs unknown pipeline %q", n.Pipeline)
	}
	start := child.Entry
	if n.From != "" {
		if child.Node(n.From) == nil {
			return fmt.Errorf("from %q is not an agent in pipeline %q", n.From, child.Name)
		}
		if !child.scopes[TopScope][n.From] {
			return fmt.Errorf("from %q must be a top-level agent of pipeline %q, not one reached only inside an orchestrator stage or session", n.From, child.Name)
		}
		start = n.From
	}
	if n.Until != "" {
		if child.Node(n.Until) == nil {
			return fmt.Errorf("until %q is not an agent in pipeline %q", n.Until, child.Name)
		}
		if !child.scopes[TopScope][n.Until] {
			return fmt.Errorf("until %q must be a top-level agent of pipeline %q, not one reached only inside an orchestrator stage or session", n.Until, child.Name)
		}
		if !handoffReachable(child, start, n.Until) {
			return fmt.Errorf("until %q is not reachable from %q in pipeline %q", n.Until, start, child.Name)
		}
	}
	if n.From != "" || n.Until != "" {
		section := child.Section(start, n.Until)
		for _, id := range keysOf(section) {
			for _, source := range child.Node(id).AdditionalInput {
				if !section[source] {
					return fmt.Errorf("section of pipeline %q includes %q, which needs additional input from %q outside the section", child.Name, id, source)
				}
			}
		}
	}
	for _, key := range keysOf(n.Params) {
		if _, ok := child.Params[key]; !ok {
			if len(child.Params) == 0 {
				return fmt.Errorf("passes param %q but pipeline %q declares no params", key, child.Name)
			}
			return fmt.Errorf("passes param %q, which pipeline %q does not declare (declared: %s)", key, child.Name, strings.Join(child.ParamNames(), ", "))
		}
	}
	for _, name := range child.RequiredParamsWithoutDefault() {
		if _, ok := n.Params[name]; !ok {
			return fmt.Errorf("must pass param %q, which pipeline %q requires", name, child.Name)
		}
	}
	return nil
}

// Section returns the agents that can run when the pipeline starts at
// start and stops at until (no stop when until is empty): everything
// reachable through handoffs, rejections, stages, and session participants,
// without following until's own handoffs.
func (p *Pipeline) Section(start, until string) map[string]bool {
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		n := p.byID[id]
		if n == nil {
			continue
		}
		if id != until {
			queue = append(queue, n.HandoffTargets()...)
		}
		if n.RejectTo != "" {
			queue = append(queue, n.RejectTo)
		}
		for _, s := range n.Stages {
			queue = append(queue, s.Agents...)
		}
		queue = append(queue, n.Participants...)
	}
	return seen
}

func handoffReachable(p *Pipeline, start, target string) bool {
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		queue = append(queue, p.byID[id].HandoffTargets()...)
	}
	return false
}

func checkReferenceCycles(pipelines map[string]*Pipeline) error {
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var stack []string
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			start := 0
			for i, s := range stack {
				if s == name {
					start = i
				}
			}
			cycle := append(append([]string{}, stack[start:]...), name)
			return fmt.Errorf("pipeline reference cycle: %s", strings.Join(cycle, " -> "))
		case done:
			return nil
		}
		p, ok := pipelines[name]
		if !ok {
			return nil
		}
		state[name] = visiting
		stack = append(stack, name)
		for _, ref := range p.References() {
			if err := visit(ref); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	for _, name := range Names(pipelines) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
