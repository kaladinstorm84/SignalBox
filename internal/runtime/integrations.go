package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// mcpRuntimes are the runtime types that can receive MCP servers.
var mcpRuntimes = map[string]bool{"codex": true, "claude": true, "copilot": true}

// baseArgs places integration flags around the configured arguments. claude
// and copilot take them before -p (copilot's -p consumes the prompt, and
// claude's list flags would swallow it); codex and generic CLIs take them
// after their subcommand.
func (c *cliRuntime) baseArgs(extra []string) []string {
	if c.kind == "claude" || c.kind == "copilot" {
		return append(append([]string{}, extra...), c.args...)
	}
	return append(append([]string{}, c.args...), extra...)
}

// integrationArgs builds the model and MCP flags for one call. env holds
// NAME=value pairs to add to the child environment; cleanup removes any
// temporary MCP configuration file.
func (c *cliRuntime) integrationArgs(req Request) (args, env []string, cleanup func(), err error) {
	cleanup = func() {}
	options, err := requestOptions(c.options, req)
	if err != nil {
		return nil, nil, cleanup, err
	}
	if req.Model == "" {
		req.Model = c.model
	}
	if req.Model != "" {
		if strings.HasPrefix(req.Model, "-") || strings.ContainsAny(req.Model, " \t\r\n") {
			return nil, nil, cleanup, fmt.Errorf("invalid model %q", req.Model)
		}
		if c.modelArg == "" {
			return nil, nil, cleanup, fmt.Errorf("runtime %q does not support per-agent models; set model_arg in %s", c.name, config.FileName)
		}
		args = append(args, c.modelArg, req.Model)
	}
	if effort := options.ReasoningEffort; effort != "" {
		switch c.kind {
		case "codex":
			args = append(args, "-c", "model_reasoning_effort="+tomlValue(effort))
		case "claude":
			args = append(args, "--effort", effort)
		case "cli":
			args = append(args, c.options.ReasoningEffortArg, effort)
		}
	}
	if speed := options.Speed; speed != "" {
		switch c.kind {
		case "codex":
			tier := "default"
			if speed == "fast" {
				tier = "fast"
			}
			args = append(args, "-c", "service_tier="+tomlValue(tier))
		case "claude":
			settings, marshalErr := json.Marshal(map[string]bool{"fastMode": speed == "fast"})
			if marshalErr != nil {
				return nil, nil, cleanup, fmt.Errorf("encoding Claude speed settings: %w", marshalErr)
			}
			args = append(args, "--settings", string(settings))
		case "cli":
			args = append(args, c.options.SpeedArg, speed)
		}
	}
	if len(req.MCP) == 0 {
		return args, nil, cleanup, nil
	}
	if !mcpRuntimes[c.kind] {
		return nil, nil, cleanup, fmt.Errorf("runtime %q (type %s) cannot use MCP servers; use a codex, claude, or copilot runtime", c.name, c.kind)
	}
	type server struct {
		name    string
		def     config.MCPServerConfig
		env     map[string]string
		envKeys []string
		tools   []string
	}
	servers := make([]server, 0, len(req.MCP))
	for _, grant := range req.MCP {
		def, ok := c.mcpServers[grant.Server]
		if !ok {
			return nil, nil, cleanup, fmt.Errorf("MCP server %q is not registered in %s", grant.Server, config.FileName)
		}
		s := server{name: grant.Server, def: def, env: map[string]string{}, tools: grant.Tools}
		for target, source := range def.EnvFrom {
			value, ok := os.LookupEnv(source)
			if !ok {
				return nil, nil, cleanup, fmt.Errorf("MCP server %q needs environment variable %s, which is not set", grant.Server, source)
			}
			s.env[target] = value
			s.envKeys = append(s.envKeys, target)
		}
		sort.Strings(s.envKeys)
		servers = append(servers, s)
	}

	switch c.kind {
	case "codex":
		// codex reads -c overrides as TOML; secrets reach the server through
		// env_vars forwarding from the child environment, not the command line.
		for _, s := range servers {
			prefix := "mcp_servers." + s.name + "."
			args = append(args,
				"-c", prefix+"command="+tomlValue(s.def.Command),
				"-c", prefix+"args="+tomlValue(nonNil(s.def.Args)),
				"-c", prefix+"enabled_tools="+tomlValue(s.tools),
			)
			if len(s.envKeys) > 0 {
				args = append(args, "-c", prefix+"env_vars="+tomlValue(s.envKeys))
				for _, key := range s.envKeys {
					env = append(env, key+"="+s.env[key])
				}
			}
		}
		return args, env, cleanup, nil
	case "claude", "copilot":
		configServers := map[string]any{}
		var allowed []string
		for _, s := range servers {
			entry := map[string]any{"command": s.def.Command, "args": nonNil(s.def.Args), "env": s.env}
			if c.kind == "claude" {
				entry["type"] = "stdio"
				for _, tool := range s.tools {
					allowed = append(allowed, "mcp__"+s.name+"__"+tool)
				}
			} else {
				entry["type"] = "local"
				entry["tools"] = s.tools
				for _, tool := range s.tools {
					allowed = append(allowed, s.name+"("+tool+")")
				}
			}
			configServers[s.name] = entry
		}
		path, err := writeMCPConfig(map[string]any{"mcpServers": configServers})
		if err != nil {
			return nil, nil, cleanup, err
		}
		cleanup = func() { _ = os.Remove(path) }
		if c.kind == "claude" {
			// --allowedTools and --mcp-config take lists, so each is followed
			// by another flag to end it.
			args = append(args, "--allowedTools", strings.Join(allowed, ","), "--mcp-config", path, "--strict-mcp-config")
		} else {
			args = append(args, "--additional-mcp-config", "@"+path, "--allow-tool", strings.Join(allowed, ","))
		}
		return args, nil, cleanup, nil
	}
	return nil, nil, cleanup, fmt.Errorf("runtime %q cannot use MCP servers", c.name)
}

// writeMCPConfig writes a per-call MCP configuration readable only by the
// current user. It may contain secret values and is removed after the call.
func writeMCPConfig(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "signalbox-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("writing MCP config: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil && !isUnsupported(err) {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("writing MCP config: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("writing MCP config: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("writing MCP config: %w", err)
	}
	return path, nil
}

func isUnsupported(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not supported")
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// tomlValue encodes a string or string list as TOML. JSON string escapes are
// valid TOML basic-string escapes.
func tomlValue(v any) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(v)
	return strings.TrimSpace(buf.String())
}

// SupportsModel reports whether agents on this runtime may set model.
func SupportsModel(rc config.RuntimeConfig) bool {
	switch rc.Type {
	case "openai", "codex", "claude", "copilot", "mock":
		return true
	case "cli":
		return rc.ModelArg != ""
	}
	return false
}

// ValidatePipeline checks a pipeline against the project configuration:
// referenced runtimes exist, code nodes are loadable, model overrides and MCP
// opt-ins are supported by the chosen runtime, and MCP grants are allowed.
func ValidatePipeline(cfg *config.Config, p *pipeline.Pipeline) error {
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.Type == pipeline.TypeCode {
			if _, err := NewCode(n, cfg.Root); err != nil {
				return err
			}
		}
		if n.Type == pipeline.TypeOpenSpec {
			if _, err := NewOpenSpec(n, cfg.Root); err != nil {
				return err
			}
		}
		if n.Type == pipeline.TypeSession && n.Retry != nil {
			for _, id := range n.Participants {
				participant := *p.Node(id)
				participant.Retry = n.Retry
				if err := validateRetryRuntimes(cfg, p, &participant); err != nil {
					return fmt.Errorf("session %q: %w", n.ID, err)
				}
			}
		}
		if n.Runtime == "" {
			continue
		}
		if err := validateRetryRuntimes(cfg, p, n); err != nil {
			return err
		}
	}
	return nil
}

func validateRetryRuntimes(cfg *config.Config, p *pipeline.Pipeline, n *pipeline.Node) error {
	choices := []pipeline.RetryAttempt{{}}
	if n.Retry != nil {
		choices = append(choices, n.Retry.Attempts...)
	}
	for i, choice := range choices {
		name, model, effort, speed := n.Runtime, n.Model, n.ReasoningEffort, n.Speed
		if choice.Runtime != "" {
			name = choice.Runtime
		}
		if choice.Model != "" {
			model = choice.Model
		}
		if choice.ReasoningEffort != "" {
			effort = choice.ReasoningEffort
		}
		if choice.Speed != "" {
			speed = choice.Speed
		}
		rc, ok := cfg.Runtimes[name]
		if !ok {
			return fmt.Errorf("pipeline %q: agent %q references runtime %q which is not defined in %s", p.Name, n.ID, name, config.FileName)
		}
		if model != "" && !SupportsModel(rc) {
			return fmt.Errorf("pipeline %q: agent %q sets model but runtime %q (type cli) has no model_arg", p.Name, n.ID, name)
		}
		// Parameterized values are checked again after substitution at invocation.
		literal := func(value string) string {
			if strings.Contains(value, "${{") {
				return ""
			}
			return value
		}
		if _, err := requestOptions(rc, Request{Model: literal(model), ReasoningEffort: literal(effort), Speed: literal(speed)}); err != nil {
			return fmt.Errorf("pipeline %q: agent %q call choice %d runtime %q: %w", p.Name, n.ID, i, name, err)
		}
		if n.FilesMode() == pipeline.FilesPath && rc.Type == "openai" {
			return fmt.Errorf("pipeline %q: agent %q uses files: path but runtime %q is type openai, which cannot read local files; use files: inline or a CLI runtime", p.Name, n.ID, name)
		}
		if len(n.MCP) > 0 {
			if !mcpRuntimes[rc.Type] {
				return fmt.Errorf("pipeline %q: agent %q uses mcp but runtime %q is type %s; MCP needs a codex, claude, or copilot runtime", p.Name, n.ID, name, rc.Type)
			}
			if _, err := grantMCP(cfg.MCP, n.ID, n.MCP); err != nil {
				return fmt.Errorf("pipeline %q: %w", p.Name, err)
			}
		}
	}
	return nil
}

// ValidateProject validates every pipeline against the config and checks
// that watchers start pipelines they can supply parameters for.
func ValidateProject(cfg *config.Config, pipelines map[string]*pipeline.Pipeline) error {
	for _, name := range pipeline.Names(pipelines) {
		if err := ValidatePipeline(cfg, pipelines[name]); err != nil {
			return err
		}
	}
	for _, w := range cfg.Watchers {
		p, ok := pipelines[w.Pipeline]
		if !ok {
			return fmt.Errorf("watcher %q references unknown pipeline %q", w.Name, w.Pipeline)
		}
		if required := p.RequiredParamsWithoutDefault(); len(required) > 0 {
			return fmt.Errorf("watcher %q starts pipeline %q, which requires param(s) %s; watchers use defaults, so give them defaults", w.Name, w.Pipeline, strings.Join(required, ", "))
		}
		if p.Files != nil && p.Files.Required {
			return fmt.Errorf("watcher %q starts pipeline %q, which requires uploaded files; watchers cannot attach files", w.Name, w.Pipeline)
		}
	}
	return nil
}
