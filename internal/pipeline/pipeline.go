// Package pipeline defines the YAML schema for SignalBox pipelines and
// validates them into an executable graph.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// NodeType distinguishes the three kinds of pipeline nodes.
type NodeType string

const (
	// TypeStandard is a single-shot agent: it accepts a handoff (or joins
	// several, see JoinCount), runs its prompt against a runtime, and hands
	// off to one or more targets.
	TypeStandard NodeType = "standard"
	// TypeOrchestrator is a long-running coordinator. It accepts handoffs
	// from many agents (fan-in) and runs its stages sequentially, dispatching
	// to agents in each stage and collecting their results.
	TypeOrchestrator NodeType = "orchestrator"
	// TypeApproval is a human gate. Execution pauses until a person approves
	// or rejects via the web UI (or the terminal in `signalbox run`).
	TypeApproval NodeType = "approval"
)

// Stage is one phase of an orchestrator. The orchestrator dispatches its
// current context to every agent listed in the stage (in parallel), waits for
// all resulting chains to terminate, then optionally synthesises the collected
// outputs with its own runtime using the stage prompt.
type Stage struct {
	ID     string   `yaml:"id"`
	Agents []string `yaml:"agents"`
	// Prompt, when set, is run against the orchestrator's runtime with the
	// collected stage outputs as input, producing the context for the next
	// stage. When empty the raw collected outputs are concatenated.
	Prompt string `yaml:"prompt,omitempty"`
}

// Node is a single agent, orchestrator, or approval gate in a pipeline.
type Node struct {
	ID          string   `yaml:"id"`
	Type        NodeType `yaml:"type"`
	Description string   `yaml:"description,omitempty"`

	// Runtime names an entry in signalbox.yaml's runtimes map. Required for
	// standard agents; optional for orchestrators (only needed when a stage
	// or the orchestrator itself has a prompt); ignored for approvals.
	Runtime string `yaml:"runtime,omitempty"`

	// Prompt is the system prompt for standard agents. For orchestrators it
	// is an optional final-synthesis prompt applied after the last stage.
	Prompt string `yaml:"prompt,omitempty"`

	// Handoff lists node IDs that receive this node's output. A standard
	// agent may hand off to several standard/approval nodes (parallel
	// branches) or to a single orchestrator. Empty means the chain
	// terminates here (and, inside an orchestrator stage, hands back to the
	// orchestrator).
	Handoff []string `yaml:"handoff,omitempty"`
	// AdditionalInput lists outputs that this node also requires in its input.
	AdditionalInput []string `yaml:"additional-input,omitempty"`
	// Format requests rendered Markdown presentation for this node's output.
	Format bool `yaml:"format,omitempty"`

	// Stages is only valid for orchestrators.
	Stages []Stage `yaml:"stages,omitempty"`
}

// Pipeline is one YAML file in the pipelines directory.
type Pipeline struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// Entry is the node that receives the initial input.
	Entry string `yaml:"entry"`
	Nodes []Node `yaml:"agents"`

	byID map[string]*Node
	// joins maps a scope key (TopScope or StageScope) to the number of
	// deliveries each non-orchestrator node receives within that scope.
	joins map[string]map[string]int
}

// TopScope is the scope key for nodes running outside any orchestrator stage.
const TopScope = ""

// StageScope returns the scope key for chains started by one orchestrator
// stage.
func StageScope(orchID, stageID string) string {
	return orchID + "/" + stageID
}

// JoinCount returns how many deliveries a non-orchestrator node receives in
// the given scope. A node with more than one waits for all of them (fan-in)
// and then runs once on the combined input.
func (p *Pipeline) JoinCount(scope, id string) int {
	return p.joins[scope][id]
}

// computeJoins counts, per scope, the handoff edges into each node from
// nodes that run in that scope (plus the scope's own dispatches). Every
// node in a scope runs exactly once, so each counted edge delivers once.
func (p *Pipeline) computeJoins() {
	p.joins = map[string]map[string]int{}
	count := func(scope string, roots []string, throughOrchs bool) {
		counts := map[string]int{}
		seen := map[string]bool{}
		queue := append([]string{}, roots...)
		for _, r := range roots {
			counts[r]++
		}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if seen[id] {
				continue
			}
			seen[id] = true
			n := p.byID[id]
			if n.Type == TypeOrchestrator && !throughOrchs {
				continue
			}
			for _, t := range n.Handoff {
				counts[t]++
				queue = append(queue, t)
			}
		}
		p.joins[scope] = counts
	}
	count(TopScope, []string{p.Entry}, true)
	for i := range p.Nodes {
		n := &p.Nodes[i]
		for _, s := range n.Stages {
			count(StageScope(n.ID, s.ID), s.Agents, false)
		}
	}
}

// Node returns the node with the given ID, or nil.
func (p *Pipeline) Node(id string) *Node {
	return p.byID[id]
}

// InboundCount returns how many static handoff edges point at the node with
// the given ID. Stage dispatches do not count: they are calls from the
// orchestrator, not handoffs into it.
func (p *Pipeline) InboundCount(id string) int {
	n := 0
	for i := range p.Nodes {
		for _, t := range p.Nodes[i].Handoff {
			if t == id {
				n++
			}
		}
	}
	return n
}

// Load reads and validates a single pipeline YAML file.
func Load(path string) (*Pipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse validates pipeline YAML from memory.
func Parse(data []byte) (*Pipeline, error) {
	var p Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// LoadDir loads every *.yaml / *.yml file in dir, keyed by pipeline name.
func LoadDir(dir string) (map[string]*Pipeline, []error) {
	out := map[string]*Pipeline{}
	var errs []error
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, []error{err}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		p, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if _, dup := out[p.Name]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate pipeline name %q", e.Name(), p.Name))
			continue
		}
		out[p.Name] = p
	}
	return out, errs
}

// Names returns sorted pipeline names from a LoadDir result.
func Names(m map[string]*Pipeline) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (p *Pipeline) validate() error {
	if p.Name == "" {
		return fmt.Errorf("pipeline is missing a name")
	}
	if len(p.Nodes) == 0 {
		return fmt.Errorf("pipeline %q has no agents", p.Name)
	}
	p.byID = map[string]*Node{}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.ID == "" {
			return fmt.Errorf("pipeline %q: agent #%d is missing an id", p.Name, i+1)
		}
		if _, dup := p.byID[n.ID]; dup {
			return fmt.Errorf("pipeline %q: duplicate agent id %q", p.Name, n.ID)
		}
		if n.Type == "" {
			n.Type = TypeStandard
		}
		switch n.Type {
		case TypeStandard, TypeOrchestrator, TypeApproval:
		default:
			return fmt.Errorf("pipeline %q: agent %q has unknown type %q (want standard, orchestrator, or approval)", p.Name, n.ID, n.Type)
		}
		p.byID[n.ID] = n
	}

	if p.Entry == "" {
		p.Entry = p.Nodes[0].ID
	}
	if _, ok := p.byID[p.Entry]; !ok {
		return fmt.Errorf("pipeline %q: entry %q is not a defined agent", p.Name, p.Entry)
	}

	for i := range p.Nodes {
		n := &p.Nodes[i]
		if err := p.validateNode(n); err != nil {
			return fmt.Errorf("pipeline %q: %w", p.Name, err)
		}
	}

	if err := p.checkDependencyCycle(); err != nil {
		return fmt.Errorf("pipeline %q: %w", p.Name, err)
	}
	if err := p.checkStageClosures(); err != nil {
		return fmt.Errorf("pipeline %q: %w", p.Name, err)
	}
	if err := p.checkAdditionalInputStageOrder(); err != nil {
		return fmt.Errorf("pipeline %q: %w", p.Name, err)
	}
	if err := p.checkReachability(); err != nil {
		return fmt.Errorf("pipeline %q: %w", p.Name, err)
	}
	p.computeJoins()
	return nil
}

func (p *Pipeline) checkAdditionalInputStageOrder() error {
	for i := range p.Nodes {
		orch := &p.Nodes[i]
		if orch.Type != TypeOrchestrator {
			continue
		}
		stageNodes := make([]map[string]bool, len(orch.Stages))
		for stageIndex, stage := range orch.Stages {
			seen := map[string]bool{}
			queue := append([]string{}, stage.Agents...)
			for len(queue) > 0 {
				id := queue[0]
				queue = queue[1:]
				if seen[id] {
					continue
				}
				seen[id] = true
				queue = append(queue, p.byID[id].Handoff...)
			}
			stageNodes[stageIndex] = seen
		}
		for stageIndex, agents := range stageNodes {
			for agentID := range agents {
				for _, sourceID := range p.byID[agentID].AdditionalInput {
					for laterStage := stageIndex + 1; laterStage < len(stageNodes); laterStage++ {
						if stageNodes[laterStage][sourceID] {
							return fmt.Errorf("orchestrator %q: agent %q in stage %q requires output from later stage agent %q", orch.ID, agentID, orch.Stages[stageIndex].ID, sourceID)
						}
					}
				}
			}
		}
	}
	return nil
}

// checkReachability requires every node to be reachable from the entry via
// handoff or stage edges. An unreachable node would never run, and an
// orchestrator expecting a handoff from it would wait forever.
func (p *Pipeline) checkReachability() error {
	seen := map[string]bool{}
	queue := []string{p.Entry}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		n := p.byID[id]
		queue = append(queue, n.Handoff...)
		for _, s := range n.Stages {
			queue = append(queue, s.Agents...)
		}
	}
	for id := range p.byID {
		if !seen[id] {
			return fmt.Errorf("agent %q is not reachable from entry %q", id, p.Entry)
		}
	}
	return nil
}

// checkStageClosures rejects orchestrators reachable (via handoff edges) from
// another orchestrator's stage agents. Chains started by a stage hand their
// terminal output back to the coordinating orchestrator, so routing them into
// a second orchestrator would create ambiguous ownership of the result.
func (p *Pipeline) checkStageClosures() error {
	for i := range p.Nodes {
		orch := &p.Nodes[i]
		if orch.Type != TypeOrchestrator {
			continue
		}
		for _, s := range orch.Stages {
			seen := map[string]bool{}
			queue := append([]string{}, s.Agents...)
			for len(queue) > 0 {
				id := queue[0]
				queue = queue[1:]
				if seen[id] {
					continue
				}
				seen[id] = true
				n := p.byID[id]
				if n.Type == TypeOrchestrator {
					return fmt.Errorf("orchestrator %q: stage %q leads to orchestrator %q via handoffs; chains inside a stage must hand back to their own orchestrator", orch.ID, s.ID, id)
				}
				queue = append(queue, n.Handoff...)
			}
		}
	}
	return nil
}

func (p *Pipeline) validateNode(n *Node) error {
	seenAdditional := map[string]bool{}
	for _, id := range n.AdditionalInput {
		if _, ok := p.byID[id]; !ok {
			return fmt.Errorf("agent %q requires additional input from unknown agent %q", n.ID, id)
		}
		if id == n.ID {
			return fmt.Errorf("agent %q may not require additional input from itself", n.ID)
		}
		if seenAdditional[id] {
			return fmt.Errorf("agent %q lists additional input from %q more than once", n.ID, id)
		}
		seenAdditional[id] = true
	}

	for _, t := range n.Handoff {
		if _, ok := p.byID[t]; !ok {
			return fmt.Errorf("agent %q hands off to unknown agent %q", n.ID, t)
		}
		if t == n.ID {
			return fmt.Errorf("agent %q hands off to itself", n.ID)
		}
	}

	// A handoff set may contain multiple standard/approval targets, but an
	// orchestrator target must be the only one: its fan-in join semantics
	// assume the whole output goes to it.
	orchTargets := 0
	for _, t := range n.Handoff {
		if p.byID[t].Type == TypeOrchestrator {
			orchTargets++
		}
	}
	if orchTargets > 0 && len(n.Handoff) > 1 {
		return fmt.Errorf("agent %q hands off to an orchestrator alongside other targets; an orchestrator handoff must be the sole target", n.ID)
	}

	switch n.Type {
	case TypeStandard:
		if n.Runtime == "" {
			return fmt.Errorf("standard agent %q is missing a runtime", n.ID)
		}
		if len(n.Stages) > 0 {
			return fmt.Errorf("standard agent %q may not define stages", n.ID)
		}
	case TypeApproval:
		if len(n.Stages) > 0 {
			return fmt.Errorf("approval %q may not define stages", n.ID)
		}
	case TypeOrchestrator:
		if len(n.Stages) == 0 {
			return fmt.Errorf("orchestrator %q has no stages", n.ID)
		}
		needsRuntime := n.Prompt != ""
		seen := map[string]bool{}
		for si, s := range n.Stages {
			if s.ID == "" {
				return fmt.Errorf("orchestrator %q: stage #%d is missing an id", n.ID, si+1)
			}
			if seen[s.ID] {
				return fmt.Errorf("orchestrator %q: duplicate stage id %q", n.ID, s.ID)
			}
			seen[s.ID] = true
			if len(s.Agents) == 0 {
				return fmt.Errorf("orchestrator %q: stage %q lists no agents", n.ID, s.ID)
			}
			if s.Prompt != "" {
				needsRuntime = true
			}
			for _, a := range s.Agents {
				target, ok := p.byID[a]
				if !ok {
					return fmt.Errorf("orchestrator %q: stage %q dispatches to unknown agent %q", n.ID, s.ID, a)
				}
				if a == n.ID {
					return fmt.Errorf("orchestrator %q: stage %q dispatches to itself", n.ID, s.ID)
				}
				if target.Type == TypeOrchestrator {
					return fmt.Errorf("orchestrator %q: stage %q dispatches to orchestrator %q; nested orchestrators are not supported", n.ID, s.ID, a)
				}
			}
		}
		if needsRuntime && n.Runtime == "" {
			return fmt.Errorf("orchestrator %q uses prompts but has no runtime", n.ID)
		}
	}
	return nil
}

// checkDependencyCycle rejects cycles over handoff, stage, and additional
// input dependencies, since execution would never terminate.
func (p *Pipeline) checkDependencyCycle() error {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(id string, trail []string) error
	visit = func(id string, trail []string) error {
		switch color[id] {
		case grey:
			return fmt.Errorf("pipeline dependency cycle detected: %s -> %s", strings.Join(trail, " -> "), id)
		case black:
			return nil
		}
		color[id] = grey
		trail = append(trail, id)
		n := p.byID[id]
		next := append([]string{}, n.Handoff...)
		for _, s := range n.Stages {
			next = append(next, s.Agents...)
		}
		for _, target := range p.Nodes {
			for _, inputID := range target.AdditionalInput {
				if inputID == id {
					next = append(next, target.ID)
				}
			}
		}
		for _, t := range next {
			if err := visit(t, trail); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}
	for id := range p.byID {
		if err := visit(id, nil); err != nil {
			return err
		}
	}
	return nil
}
