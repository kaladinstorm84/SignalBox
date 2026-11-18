// Package exporter renders validated pipelines as standalone programs.
package exporter

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/knowledgebase"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/plugin"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

//go:embed python.tmpl
var pythonRunner string

type pythonNode struct {
	ID              string                  `json:"id"`
	Type            pipeline.NodeType       `json:"type"`
	Prompt          string                  `json:"prompt"`
	AdditionalInput []string                `json:"additional_input"`
	Context         *pipeline.ContextConfig `json:"context"`
	RejectTo        string                  `json:"reject_to"`
	MaxReworks      int                     `json:"max_reworks"`
	Command         *runtime.CLICommand     `json:"command"`
	Knowledge       string                  `json:"knowledge"`
	Stages          []pythonStage           `json:"stages,omitempty"`
	Plugin          *pythonPlugin           `json:"plugin,omitempty"`
}

type pythonStage struct {
	ID       string         `json:"id"`
	Prompt   string         `json:"prompt"`
	Branches [][]pythonNode `json:"branches"`
}

type pythonPlugin struct {
	Definition *plugin.Definition `json:"definition"`
	Files      map[string][]byte  `json:"files"`
}

type pythonPipeline struct {
	Name               string                     `json:"name"`
	Params             map[string]*pipeline.Param `json:"params"`
	Nodes              []pythonNode               `json:"nodes"`
	MaxConcurrentCalls int                        `json:"max_concurrent_calls"`
}

// Python snapshots the supported execution semantics. Unsupported features
// are errors rather than omissions from the generated program.
func Python(p *pipeline.Pipeline, cfg *config.Config) ([]byte, error) {
	if p.Input != nil || p.Files != nil {
		return nil, fmt.Errorf("Python export does not support input schemas or uploaded files")
	}
	for name, param := range p.Params {
		if param.Type == pipeline.ParamFile {
			return nil, fmt.Errorf("Python export does not support file parameter %q", name)
		}
	}
	spec := pythonPipeline{Name: p.Name, Params: p.Params, MaxConcurrentCalls: cfg.MaxConcurrentCalls}
	if spec.MaxConcurrentCalls <= 0 {
		spec.MaxConcurrentCalls = 4
	}
	used := map[string]bool{}
	nodes, err := pythonChain(p, cfg, p.Entry, map[string]bool{}, used, false)
	if err != nil {
		return nil, err
	}
	spec.Nodes = nodes
	if len(used) != len(p.Nodes) {
		return nil, fmt.Errorf("Python export requires all nodes to belong to the main chain or an orchestrator stage")
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encoding Python export: %w", err)
	}
	return []byte(strings.Replace(pythonRunner, "__SIGNALBOX_SPEC__", base64.StdEncoding.EncodeToString(data), 1)), nil
}

func pythonChain(p *pipeline.Pipeline, cfg *config.Config, entry string, earlier, used map[string]bool, parallel bool) ([]pythonNode, error) {
	available := map[string]bool{}
	for id := range earlier {
		available[id] = true
	}
	var nodes []pythonNode
	positions := map[string]int{}
	for id := entry; id != ""; {
		if used[id] {
			return nil, fmt.Errorf("Python export: node %q is shared by branches or forms a cycle; joins are not supported", id)
		}
		n := p.Node(id)
		if n == nil {
			return nil, fmt.Errorf("Python export: unknown node %q", id)
		}
		used[id] = true
		positions[id] = len(nodes)
		switch n.Type.BaseType() {
		case pipeline.TypeStandard, pipeline.TypeLLMGate, pipeline.TypeApproval, pipeline.TypeOrchestrator:
		case pipeline.TypeCode:
			if !n.Type.IsPlugin() {
				return nil, fmt.Errorf("Python export does not support node %q (type %s); package scripts as plugins", id, n.Type)
			}
		default:
			return nil, fmt.Errorf("Python export does not support node %q (type %s)", id, n.Type)
		}
		if n.Retry != nil || n.Output != nil || len(n.MCP) > 0 || (n.Files != "" && n.Files != "none") {
			return nil, fmt.Errorf("Python export: node %q uses unsupported retries, schemas, MCP, or files", id)
		}
		if parallel && n.Context != nil && n.Context.Strategy != pipeline.ContextNone && n.Context.Strategy != pipeline.ContextOwn {
			return nil, fmt.Errorf("Python export: node %q uses cross-branch context; parallel stage branches support only none or own context", id)
		}
		if parallel && n.Type == pipeline.TypeApproval {
			return nil, fmt.Errorf("Python export: approval %q must be in a sequential stage or after the orchestrator, not a parallel branch", id)
		}
		if len(n.Handoff) > 1 {
			return nil, fmt.Errorf("Python export: node %q has parallel handoffs; use independent orchestrator stage branches", id)
		}
		for _, route := range n.Handoff {
			if route.Condition != "" || route.Input != "" {
				return nil, fmt.Errorf("Python export: node %q uses conditional or templated handoffs", id)
			}
		}
		for _, source := range n.AdditionalInput {
			if !available[source] {
				return nil, fmt.Errorf("Python export: node %q requires additional input %q outside its earlier steps or completed stages", id, source)
			}
		}
		if n.RejectTo != "" {
			if position, ok := positions[n.RejectTo]; !ok || position >= positions[id] {
				return nil, fmt.Errorf("Python export: node %q must reject to an earlier node in the sequential chain", id)
			}
		}
		step := pythonNode{
			ID: id, Type: n.Type, Prompt: n.Prompt,
			AdditionalInput: n.AdditionalInput, Context: n.Context,
			RejectTo: n.RejectTo, MaxReworks: n.MaxReworks,
		}
		if len(n.KnowledgeBases) > 0 {
			if cfg.Root == "" {
				return nil, fmt.Errorf("Python export: node %q requires a project root to snapshot knowledge bases", id)
			}
			knowledge, err := knowledgebase.New(cfg.Root).Content(n.KnowledgeBases)
			if err != nil {
				return nil, fmt.Errorf("exporting knowledge for node %q: %w", id, err)
			}
			step.Knowledge = knowledge
		}
		needsCommand := n.Type != pipeline.TypeApproval && n.Type != pipeline.TypeOrchestrator && !n.Type.IsPlugin()
		if n.Type.IsPlugin() {
			def, files, err := plugin.Load(cfg.Root, n.Type.PluginName())
			if err != nil {
				return nil, fmt.Errorf("exporting node %q: %w", id, err)
			}
			if n.Plugin == nil || n.Plugin.Digest != def.Digest {
				return nil, fmt.Errorf("Python export: plugin %q changed since loading; reload before exporting", def.Name)
			}
			step.Plugin = &pythonPlugin{Definition: def, Files: files}
		}
		if n.Type == pipeline.TypeOrchestrator {
			if len(n.Stages) == 0 {
				return nil, fmt.Errorf("Python export: orchestrator %q has no stages", id)
			}
			needsCommand = n.Prompt != ""
			for _, stage := range n.Stages {
				exported := pythonStage{ID: stage.ID, Prompt: stage.Prompt}
				completed := map[string]bool{}
				for _, agent := range stage.Agents {
					branch, err := pythonChain(p, cfg, agent, available, used, len(stage.Agents) > 1)
					if err != nil {
						return nil, err
					}
					exported.Branches = append(exported.Branches, branch)
					for _, node := range branch {
						completed[node.ID] = true
					}
				}
				for source := range completed {
					available[source] = true
				}
				step.Stages = append(step.Stages, exported)
				needsCommand = needsCommand || stage.Prompt != ""
			}
		}
		if needsCommand {
			for _, setting := range []string{n.Model, n.ReasoningEffort, n.Speed} {
				if strings.Contains(setting, "${{") {
					return nil, fmt.Errorf("Python export: node %q uses parameterized runtime settings; use fixed model, reasoning_effort and speed values", id)
				}
			}
			rc, ok := cfg.Runtimes[n.Runtime]
			if !ok {
				return nil, fmt.Errorf("Python export: node %q uses unknown runtime %q", id, n.Runtime)
			}
			command, err := runtime.ExportCLICommand(n.Runtime, rc, runtime.Request{
				Model: n.Model, ReasoningEffort: n.ReasoningEffort,
				Speed: n.Speed, TimeoutSeconds: n.TimeoutSeconds,
			})
			if err != nil {
				return nil, fmt.Errorf("exporting node %q: %w", id, err)
			}
			step.Command = &command
		}
		nodes = append(nodes, step)
		available[id] = true
		id = ""
		if len(n.Handoff) > 0 {
			id = n.Handoff[0].To
		}
	}
	return nodes, nil
}
