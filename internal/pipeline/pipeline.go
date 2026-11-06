// Package pipeline defines the YAML schema for SignalBox pipelines and
// validates them into an executable graph.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/schema"
	"gopkg.in/yaml.v3"
)

// NodeType distinguishes the kinds of pipeline nodes.
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
	// TypeLLMGate evaluates its input with a runtime and records an automated
	// approve/reject decision.
	TypeLLMGate NodeType = "llm_gate"
	// TypeManualEdit pauses for a person to edit the incoming payload before
	// handing it off.
	TypeManualEdit NodeType = "manual_edit"
	TypeCode       NodeType = "code"
	// TypeSession runs a multi-round discussion between participant agents
	// until they reach consensus or the round limit.
	TypeSession NodeType = "session"
	// TypePipeline runs another pipeline (or a section of it) as a linked
	// child execution and hands off the child's final output.
	TypePipeline NodeType = "pipeline"
)

// MaxPipelineDepth bounds how deeply pipeline nodes may nest child
// executions at run time.
const MaxPipelineDepth = 5

// Session defaults and limits.
const (
	TurnOrderRoundRobin      = "round_robin"
	TurnOrderModerator       = "moderator"
	ConsensusVote            = "vote"
	ConsensusModeratorDecide = "moderator_decides"
	ConsensusRequireAll      = "all"
	ConsensusRequireMajority = "majority"
	DefaultConsensusKeyword  = "VOTE: AGREE"
	DefaultMaxRounds         = 5
	MaxSessionRounds         = 20
	MaxChunkTitleLength      = 200
	MaxInitialChunks         = 100
)

// Consensus configures how a session decides it has finished.
type Consensus struct {
	// Method is vote (default) or moderator_decides.
	Method string `yaml:"method,omitempty" json:"method"`
	// Keyword is the line a participant writes to vote in agreement.
	Keyword string `yaml:"keyword,omitempty" json:"keyword,omitempty"`
	// Require is all (default) or majority, for the vote method.
	Require string `yaml:"require,omitempty" json:"require,omitempty"`
	// RequireChunksDone withholds consensus while any chunk is pending.
	RequireChunksDone bool `yaml:"require_chunks_done,omitempty" json:"require_chunks_done,omitempty"`
}

// ChunkConfig enables chunk tracking for a session.
type ChunkConfig struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Initial []string `yaml:"initial,omitempty" json:"initial,omitempty"`
}

// HandoffRule is a conditional route. A YAML scalar remains a shorthand for
// an unconditional handoff, preserving existing pipeline files.
type HandoffRule struct {
	To        string `yaml:"to" json:"to"`
	Condition string `yaml:"condition,omitempty" json:"condition,omitempty"`
	Input     string `yaml:"input,omitempty" json:"input,omitempty"`
}

func (h *HandoffRule) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		h.To = node.Value
		return nil
	}
	type plain HandoffRule
	var rule plain
	if err := node.Decode(&rule); err != nil {
		return err
	}
	*h = HandoffRule(rule)
	return nil
}

// RetryPolicy controls automatic retries for runtime errors. Count is the
// number of retries after the initial call; Backoff is a Go duration string.
type RetryPolicy struct {
	Count   int    `yaml:"count" json:"count"`
	Backoff string `yaml:"backoff,omitempty" json:"backoff,omitempty"`
}

// BackoffDuration parses the configured fixed delay between automatic retries.
func (r RetryPolicy) BackoffDuration() (time.Duration, error) {
	if r.Backoff == "" {
		return 0, nil
	}
	return time.ParseDuration(r.Backoff)
}

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

// Node is a single agent, orchestrator, approval gate, or manual edit step
// in a pipeline.
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

	// Model overrides the runtime's default model for this agent's calls.
	Model string `yaml:"model,omitempty"`
	// MCP opts this agent in to MCP servers registered in signalbox.yaml.
	MCP []MCPUse `yaml:"mcp,omitempty"`

	// Handoff lists node IDs that receive this node's output. A standard
	// agent may hand off to several standard/approval nodes (parallel
	// branches) or to a single orchestrator. Empty means the chain
	// terminates here (and, inside an orchestrator stage, hands back to the
	// orchestrator).
	Handoff  []HandoffRule `yaml:"handoff,omitempty"`
	RejectTo string        `yaml:"reject-to,omitempty"`
	Retry    *RetryPolicy  `yaml:"retry,omitempty"`
	// MaxReworks bounds LLM-gate rejection loops. It is only valid on gates.
	MaxReworks int `yaml:"max_reworks,omitempty" json:"max_reworks,omitempty"`
	// AdditionalInput lists outputs that this node also requires in its input.
	AdditionalInput []string `yaml:"additional-input,omitempty"`
	// Context selects earlier execution outputs to append to runtime input.
	Context *ContextConfig `yaml:"context,omitempty"`
	// Output requires JSON output conforming to a project schema.
	Output *OutputSpec `yaml:"output,omitempty"`
	// Format requests rendered Markdown presentation for this node's output.
	Format         bool           `yaml:"format,omitempty"`
	Language       string         `yaml:"language,omitempty"`
	CodeSource     string         `yaml:"code-source,omitempty"`
	TimeoutSeconds int            `yaml:"timeout_seconds,omitempty"`
	Codes          map[string]int `yaml:"codes,omitempty"`

	// Stages is only valid for orchestrators.
	Stages []Stage `yaml:"stages,omitempty"`

	// Session fields are only valid on session nodes. Participants are
	// standard agents invoked only by the session, never by handoffs.
	Participants []string     `yaml:"participants,omitempty"`
	TurnOrder    string       `yaml:"turn_order,omitempty"`
	Moderator    string       `yaml:"moderator,omitempty"`
	MaxRounds    int          `yaml:"max_rounds,omitempty"`
	Consensus    *Consensus   `yaml:"consensus,omitempty"`
	SummaryAgent string       `yaml:"summary_agent,omitempty"`
	Chunks       *ChunkConfig `yaml:"chunks,omitempty"`

	// Pipeline fields are only valid on pipeline nodes. Pipeline names the
	// child pipeline; From and Until select a section of it (default: its
	// entry, running to completion); Params are passed to the child.
	Pipeline string            `yaml:"pipeline,omitempty"`
	From     string            `yaml:"from,omitempty"`
	Until    string            `yaml:"until,omitempty"`
	Params   map[string]string `yaml:"params,omitempty"`
}

// ChunksEnabled reports whether a session tracks chunks.
func (n *Node) ChunksEnabled() bool {
	return n.Chunks != nil && n.Chunks.Enabled
}

// TurnSequence returns the participants in the order they speak each round.
func (n *Node) TurnSequence() []string {
	if n.TurnOrder != TurnOrderModerator {
		return append([]string{}, n.Participants...)
	}
	order := []string{n.Moderator}
	for _, id := range n.Participants {
		if id != n.Moderator {
			order = append(order, id)
		}
	}
	return order
}

// HandoffTargets returns every statically configured route target.
func (n *Node) HandoffTargets() []string {
	targets := make([]string, len(n.Handoff))
	for i, route := range n.Handoff {
		targets[i] = route.To
	}
	return targets
}

// Pipeline is one YAML file in the pipelines directory.
type Pipeline struct {
	// Schema is the pipeline format version (default 1). Files declaring a
	// newer schema than this build supports are rejected.
	Schema      int    `yaml:"schema,omitempty"`
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// Version is an optional semantic version used when publishing to a
	// registry.
	Version string `yaml:"version,omitempty"`
	// Entry is the node that receives the initial input.
	Entry string `yaml:"entry"`
	Nodes []Node `yaml:"agents"`
	// Params declares typed values supplied when an execution starts and
	// referenced as ${{ params.name }}.
	Params map[string]*Param `yaml:"params,omitempty"`
	// Input optionally validates the initial input against a schema.
	Input *InputSpec `yaml:"input,omitempty"`
	// Sources lists files merged in through imports, in load order.
	Sources []string `yaml:"-"`

	byID    map[string]*Node
	schemas map[string]*schema.Schema
	// joins maps a scope key (TopScope or StageScope) to the number of
	// deliveries each non-orchestrator node receives within that scope.
	joins  map[string]map[string]int
	scopes map[string]map[string]bool
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

// JoinCounts returns a mutable copy of expected deliveries for a scope.
func (p *Pipeline) JoinCounts(scope string) map[string]int {
	counts := make(map[string]int, len(p.joins[scope]))
	for id, count := range p.joins[scope] {
		counts[id] = count
	}
	return counts
}

// computeJoins counts, per scope, the handoff edges into each node from
// nodes that run in that scope (plus the scope's own dispatches). Every
// node in a scope runs exactly once, so each counted edge delivers once.
func (p *Pipeline) computeJoins() {
	p.joins = map[string]map[string]int{}
	p.scopes = map[string]map[string]bool{}
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
			for _, t := range n.HandoffTargets() {
				counts[t]++
				queue = append(queue, t)
			}
		}
		p.joins[scope] = counts
		members := map[string]bool{}
		queue = append([]string{}, roots...)
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if members[id] {
				continue
			}
			members[id] = true
			node := p.byID[id]
			if node.Type == TypeOrchestrator && !throughOrchs {
				continue
			}
			queue = append(queue, node.HandoffTargets()...)
			if node.RejectTo != "" {
				queue = append(queue, node.RejectTo)
			}
		}
		p.scopes[scope] = members
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
		for _, t := range p.Nodes[i].HandoffTargets() {
			if t == id {
				n++
			}
		}
	}
	return n
}

// Load reads and validates a single pipeline YAML file. Imports resolve
// relative to the file and, by default, must stay inside its directory.
func Load(path string, opts ...Options) (*Pipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	return ParseFile(data, path, o)
}

// Parse validates pipeline YAML from memory. Imports are not available
// without a file location; use ParseFile.
func Parse(data []byte) (*Pipeline, error) {
	return parse(data, "", Options{})
}

// LoadDir loads every *.yaml / *.yml file in dir, keyed by pipeline name.
func LoadDir(dir string, opts ...Options) (map[string]*Pipeline, []error) {
	out := map[string]*Pipeline{}
	var errs []error
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, []error{err}
	}
	if len(opts) > 0 && opts[0].Schemas == nil && opts[0].SchemasDir != "" {
		opts = []Options{opts[0]}
		opts[0].Schemas, _ = schema.LoadDir(opts[0].SchemasDir)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		p, err := Load(filepath.Join(dir, e.Name()), opts...)
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
	errs = append(errs, CheckReferences(out)...)
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
	if p.Schema < 0 || p.Schema > SchemaVersion {
		return fmt.Errorf("pipeline %q uses schema %d; this SignalBox supports schema %d or lower (upgrade SignalBox)", p.Name, p.Schema, SchemaVersion)
	}
	if p.Version != "" && !ValidVersion(p.Version) {
		return fmt.Errorf("pipeline %q: version %q must be a semantic version such as 1.2.0", p.Name, p.Version)
	}
	if len(p.Nodes) == 0 {
		return fmt.Errorf("pipeline %q has no agents", p.Name)
	}
	if err := p.validateParams(); err != nil {
		return fmt.Errorf("pipeline %q: %w", p.Name, err)
	}
	if err := p.validateInputSpec(); err != nil {
		return fmt.Errorf("pipeline %q: %w", p.Name, err)
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
		case TypeStandard, TypeOrchestrator, TypeApproval, TypeLLMGate, TypeManualEdit, TypeCode, TypeSession, TypePipeline:
		default:
			return fmt.Errorf("pipeline %q: agent %q has unknown type %q (want standard, orchestrator, approval, llm_gate, manual_edit, code, session, or pipeline)", p.Name, n.ID, n.Type)
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

	if err := p.checkSessionParticipants(); err != nil {
		return fmt.Errorf("pipeline %q: %w", p.Name, err)
	}
	if err := p.checkDependencyCycle(); err != nil {
		return fmt.Errorf("pipeline %q: %w", p.Name, err)
	}
	p.computeJoins()
	if err := p.checkRejectionTargets(); err != nil {
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
	return nil
}

func (p *Pipeline) checkRejectionTargets() error {
	for scope, members := range p.scopes {
		for id := range members {
			if p.joins[scope][id] > 0 {
				continue
			}
			for _, normalCounts := range p.joins {
				if normalCounts[id] > 0 {
					return fmt.Errorf("rejection route to %q must stay in the same stage/scope", id)
				}
			}
		}
	}
	for _, node := range p.Nodes {
		if node.RejectTo == "" {
			continue
		}
		seen := map[string]bool{}
		queue := []string{node.RejectTo}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if seen[id] {
				continue
			}
			seen[id] = true
			queue = append(queue, p.byID[id].HandoffTargets()...)
		}
		if !seen[node.ID] {
			return fmt.Errorf("gate %q: reject-to %q must lead back to this gate via handoffs", node.ID, node.RejectTo)
		}
	}
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
				queue = append(queue, p.byID[id].HandoffTargets()...)
				if p.byID[id].RejectTo != "" {
					queue = append(queue, p.byID[id].RejectTo)
				}
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
// handoff, stage, or rejection edges. An unreachable node would never run, and an
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
		queue = append(queue, n.HandoffTargets()...)
		if n.RejectTo != "" {
			queue = append(queue, n.RejectTo)
		}
		for _, s := range n.Stages {
			queue = append(queue, s.Agents...)
		}
		queue = append(queue, n.Participants...)
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
				queue = append(queue, n.HandoffTargets()...)
				if n.RejectTo != "" {
					queue = append(queue, n.RejectTo)
				}
			}
		}
	}
	return nil
}

func (p *Pipeline) validateNode(n *Node) error {
	if n.RejectTo != "" {
		if n.Type != TypeApproval && n.Type != TypeLLMGate {
			return fmt.Errorf("agent %q: reject-to is only supported on approval nodes and llm_gate nodes", n.ID)
		}
		target, ok := p.byID[n.RejectTo]
		if !ok {
			return fmt.Errorf("gate %q: reject-to references unknown agent %q", n.ID, n.RejectTo)
		}
		if target.ID == n.ID || target.Type == TypeOrchestrator {
			return fmt.Errorf("gate %q: reject-to must reference another non-orchestrator node", n.ID)
		}
	}
	if n.Type != TypeCode && (n.Language != "" || n.CodeSource != "" || n.TimeoutSeconds != 0 || n.Codes != nil) {
		return fmt.Errorf("agent %q: language, code-source, timeout_seconds, and codes are only supported on code nodes", n.ID)
	}
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

	if err := validateOutputSpec(n); err != nil {
		return err
	}
	structured := n.OutputSchema() != ""
	seenRoutes := map[string]bool{}
	for i := range n.Handoff {
		route := &n.Handoff[i]
		if schema.IsFieldCondition(route.Condition) {
			if !structured {
				return fmt.Errorf("agent %q: field condition %q requires output.schema on the agent", n.ID, route.Condition)
			}
			if _, err := schema.ParseCondition(route.Condition); err != nil {
				return fmt.Errorf("agent %q: %w", n.ID, err)
			}
		}
		switch route.Condition {
		case "", "always":
		case "on_approve", "on_reject":
			if n.Type != TypeApproval && n.Type != TypeLLMGate {
				return fmt.Errorf("agent %q: handoff condition %q is only supported on approval and llm_gate nodes", n.ID, route.Condition)
			}
		case "on_consensus", "on_no_consensus":
			if n.Type != TypeSession {
				return fmt.Errorf("agent %q: handoff condition %q is only supported on session nodes", n.ID, route.Condition)
			}
		default:
			if !schema.IsFieldCondition(route.Condition) {
				return fmt.Errorf("agent %q: unknown handoff condition %q (want always, on_approve, on_reject, on_consensus, on_no_consensus, or a field comparison such as output.risk == \"high\")", n.ID, route.Condition)
			}
		}
		if route.To == "" {
			return fmt.Errorf("agent %q has a handoff with no target", n.ID)
		}
		if _, ok := p.byID[route.To]; !ok {
			return fmt.Errorf("agent %q hands off to unknown agent %q", n.ID, route.To)
		}
		if route.To == n.ID {
			return fmt.Errorf("agent %q hands off to itself", n.ID)
		}
		key := route.Condition + "\x00" + route.To
		if seenRoutes[key] {
			return fmt.Errorf("agent %q repeats handoff to %q for condition %q", n.ID, route.To, route.Condition)
		}
		seenRoutes[key] = true
		if err := validateHandoffTemplate(stripParamRefs(route.Input), structured); err != nil {
			return fmt.Errorf("agent %q handoff to %q: %w", n.ID, route.To, err)
		}
	}

	// A handoff set may contain multiple standard/approval targets, but an
	// orchestrator target must be the only one: its fan-in join semantics
	// assume the whole output goes to it.
	orchTargets := 0
	for _, route := range n.Handoff {
		if p.byID[route.To].Type == TypeOrchestrator {
			orchTargets++
		}
	}
	if orchTargets > 0 && len(n.Handoff) > 1 {
		return fmt.Errorf("agent %q hands off to an orchestrator alongside other targets; an orchestrator handoff must be the sole target", n.ID)
	}
	if n.Retry != nil {
		switch n.Type {
		case TypeStandard, TypeCode, TypeLLMGate, TypeOrchestrator, TypeSession, TypePipeline:
		default:
			return fmt.Errorf("agent %q: retry is only supported on callable nodes", n.ID)
		}
		if n.Retry.Count < 0 || n.Retry.Count > 10 {
			return fmt.Errorf("agent %q: retry.count must be between 0 and 10", n.ID)
		}
		backoff, err := n.Retry.BackoffDuration()
		if err != nil || backoff < 0 || backoff > 5*time.Minute {
			return fmt.Errorf("agent %q: retry.backoff must be a duration between 0 and 5m", n.ID)
		}
	}
	if n.Type != TypeLLMGate && n.MaxReworks != 0 {
		return fmt.Errorf("agent %q: max_reworks is only supported on llm_gate nodes", n.ID)
	}
	if n.MaxReworks < 0 || n.MaxReworks > 10 {
		return fmt.Errorf("llm_gate %q: max_reworks must be between 0 and 10", n.ID)
	}
	if err := validateContext(n); err != nil {
		return err
	}
	if err := validateIntegrations(n); err != nil {
		return err
	}

	if n.Type != TypeSession && (len(n.Participants) > 0 || n.TurnOrder != "" || n.Moderator != "" || n.MaxRounds != 0 || n.Consensus != nil || n.SummaryAgent != "" || n.Chunks != nil) {
		return fmt.Errorf("agent %q: participants, turn_order, moderator, max_rounds, consensus, summary_agent, and chunks are only supported on session nodes", n.ID)
	}
	if n.Type != TypePipeline && (n.Pipeline != "" || n.From != "" || n.Until != "" || n.Params != nil) {
		return fmt.Errorf("agent %q: pipeline, from, until, and params are only supported on pipeline nodes", n.ID)
	}

	switch n.Type {
	case TypePipeline:
		if strings.TrimSpace(n.Pipeline) == "" {
			return fmt.Errorf("pipeline agent %q is missing pipeline (the name of the pipeline to run)", n.ID)
		}
		if n.Runtime != "" || n.Prompt != "" || len(n.Stages) > 0 {
			return fmt.Errorf("pipeline agent %q may not define runtime, prompt, or stages", n.ID)
		}
		for name := range n.Params {
			if !paramNamePattern.MatchString(name) {
				return fmt.Errorf("pipeline agent %q: param %q is not a valid param name", n.ID, name)
			}
		}
	case TypeSession:
		return p.validateSession(n)
	case TypeCode:
		if n.Codes != nil {
			if len(n.Codes) != 3 {
				return fmt.Errorf("code agent %q: codes must define success, fail, and retry", n.ID)
			}
			seenCodes := map[int]bool{}
			for _, outcome := range []string{"success", "fail", "retry"} {
				code, ok := n.Codes[outcome]
				if !ok || code < 0 {
					return fmt.Errorf("code agent %q: codes.%s must be a non-negative exit code", n.ID, outcome)
				}
				if seenCodes[code] {
					return fmt.Errorf("code agent %q: exit codes must be distinct", n.ID)
				}
				seenCodes[code] = true
			}
		}
		if n.CodeSource == "" {
			return fmt.Errorf("code agent %q is missing code-source", n.ID)
		}
		switch n.Language {
		case "python", "javascript", "powershell":
		default:
			return fmt.Errorf("code agent %q: language must be python, javascript, or powershell", n.ID)
		}
		if n.Runtime != "" || n.Prompt != "" || len(n.Stages) > 0 {
			return fmt.Errorf("code agent %q may not define runtime, prompt, or stages", n.ID)
		}
		if n.TimeoutSeconds < 0 {
			return fmt.Errorf("code agent %q: timeout_seconds must not be negative", n.ID)
		}
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
	case TypeLLMGate:
		if n.Runtime == "" {
			return fmt.Errorf("llm_gate %q is missing a runtime", n.ID)
		}
		if strings.TrimSpace(n.Prompt) == "" {
			return fmt.Errorf("llm_gate %q is missing a prompt", n.ID)
		}
		if len(n.Stages) > 0 {
			return fmt.Errorf("llm_gate %q may not define stages", n.ID)
		}
		if n.MaxReworks > 0 && n.RejectTo == "" {
			return fmt.Errorf("llm_gate %q: max_reworks requires reject-to", n.ID)
		}
		if n.RejectTo != "" && n.MaxReworks == 0 {
			return fmt.Errorf("llm_gate %q: reject-to requires a positive max_reworks limit", n.ID)
		}
	case TypeManualEdit:
		if n.Runtime != "" || n.Prompt != "" || len(n.Stages) > 0 {
			return fmt.Errorf("manual_edit %q may not define runtime, prompt, or stages", n.ID)
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

func validateHandoffTemplate(template string, structured bool) error {
	for remaining := template; ; {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			if strings.Contains(remaining, "}}") {
				return fmt.Errorf("malformed input template")
			}
			return nil
		}
		if strings.Contains(remaining[:start], "}}") {
			return fmt.Errorf("malformed input template")
		}
		end := strings.Index(remaining[start+2:], "}}")
		if end < 0 {
			return fmt.Errorf("malformed input template")
		}
		name := strings.TrimSpace(remaining[start+2 : start+2+end])
		switch name {
		case "input", "output", "comment":
		default:
			_, isField, err := schema.ParseFieldRef(name)
			if !isField {
				return fmt.Errorf("unknown input template value %q (want input, output, output.<field>, or comment)", name)
			}
			if err != nil {
				return err
			}
			if !structured {
				return fmt.Errorf("input template %q requires output.schema on the agent", name)
			}
		}
		remaining = remaining[start+2+end+2:]
	}
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
		next := append([]string{}, n.HandoffTargets()...)
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
