// Package exporter renders validated pipelines as standalone programs.
package exporter

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
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
}

type pythonPipeline struct {
	Name   string                     `json:"name"`
	Params map[string]*pipeline.Param `json:"params"`
	Nodes  []pythonNode               `json:"nodes"`
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
	spec := pythonPipeline{Name: p.Name, Params: p.Params}
	positions := map[string]int{}
	for id := p.Entry; id != ""; {
		if _, seen := positions[id]; seen {
			return nil, fmt.Errorf("Python export requires an acyclic, sequential handoff chain")
		}
		n := p.Node(id)
		if n == nil {
			return nil, fmt.Errorf("Python export: unknown node %q", id)
		}
		positions[id] = len(spec.Nodes)
		switch n.Type {
		case pipeline.TypeStandard, pipeline.TypeLLMGate, pipeline.TypeApproval:
		default:
			return nil, fmt.Errorf("Python export does not support node %q (type %s)", id, n.Type)
		}
		if n.Retry != nil || n.Output != nil || len(n.MCP) > 0 || len(n.KnowledgeBases) > 0 || (n.Files != "" && n.Files != "none") {
			return nil, fmt.Errorf("Python export: node %q uses unsupported retries, schemas, MCP, knowledge bases, or files", id)
		}
		if len(n.Handoff) > 1 {
			return nil, fmt.Errorf("Python export: node %q has parallel handoffs; only sequential pipelines are supported", id)
		}
		for _, route := range n.Handoff {
			if route.Condition != "" || route.Input != "" {
				return nil, fmt.Errorf("Python export: node %q uses conditional or templated handoffs", id)
			}
		}
		for _, source := range n.AdditionalInput {
			if position, ok := positions[source]; !ok || position >= positions[id] {
				return nil, fmt.Errorf("Python export: node %q requires additional input %q outside its earlier sequential steps", id, source)
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
		if n.Type != pipeline.TypeApproval {
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
		spec.Nodes = append(spec.Nodes, step)
		id = ""
		if len(n.Handoff) > 0 {
			id = n.Handoff[0].To
		}
	}
	if len(spec.Nodes) != len(p.Nodes) {
		return nil, fmt.Errorf("Python export requires all nodes to belong to one sequential handoff chain")
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encoding Python export: %w", err)
	}
	return []byte(strings.Replace(pythonRunner, "__SIGNALBOX_SPEC__", base64.StdEncoding.EncodeToString(data), 1)), nil
}
