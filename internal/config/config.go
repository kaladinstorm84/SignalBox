// Package config loads the project-level signalbox.yaml file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FileName is the project configuration file created by `signalbox init`.
const FileName = "signalbox.yaml"

const DefaultMaxConcurrentCalls = 4

// RuntimeConfig describes one runtime entry in signalbox.yaml.
type RuntimeConfig struct {
	// Type is one of: openai, codex, claude, copilot, cli, mock.
	// codex/claude/copilot are presets over the generic cli type.
	Type string `yaml:"type"`

	// --- openai ---
	Model     string `yaml:"model,omitempty"`
	BaseURL   string `yaml:"base_url,omitempty"`
	APIKeyEnv string `yaml:"api_key_env,omitempty"`

	// --- cli / codex / claude / copilot ---
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
	// PromptMode is "arg" (prompt appended as final argument, default) or
	// "stdin" (prompt piped to the process).
	PromptMode string `yaml:"prompt_mode,omitempty"`
	// TimeoutSeconds bounds a single runtime invocation. Default 600.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
	// ModelArg is the flag a generic cli runtime uses to receive an agent's
	// model override (for example --model). Presets already know theirs.
	ModelArg string `yaml:"model_arg,omitempty"`
}

// MCPConfig registers MCP servers agents may opt in to. Nothing is passed to
// an agent CLI unless Enabled is true and the agent lists the server.
type MCPConfig struct {
	Enabled bool                       `yaml:"enabled,omitempty"`
	Servers map[string]MCPServerConfig `yaml:"servers,omitempty"`
}

// MCPServerConfig describes a stdio MCP server. EnvFrom maps the variable the
// server receives to the SignalBox process environment variable holding its
// value, so secrets never appear in configuration or execution history.
type MCPServerConfig struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args,omitempty"`
	EnvFrom map[string]string `yaml:"env_from,omitempty"`
	// Tools is the allow-list of tools agents may be granted.
	Tools []string `yaml:"tools"`
}

var (
	mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	mcpToolPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ServerConfig controls `signalbox serve`.
type ServerConfig struct {
	Host string `yaml:"host,omitempty"`
	Port int    `yaml:"port,omitempty"`
}

// WatcherConfig describes a command polled to trigger a pipeline.
type WatcherConfig struct {
	Name           string   `yaml:"name"`
	Command        string   `yaml:"command"`
	Args           []string `yaml:"args,omitempty"`
	Interval       string   `yaml:"interval"`
	TimeoutSeconds int      `yaml:"timeout_seconds,omitempty"`
	Pipeline       string   `yaml:"pipeline"`
}

// Config is the parsed signalbox.yaml.
type Config struct {
	Version            int                      `yaml:"version"`
	Project            string                   `yaml:"project"`
	PipelinesDir       string                   `yaml:"pipelines_dir,omitempty"`
	StateDir           string                   `yaml:"state_dir,omitempty"`
	ExecutionRetention *int                     `yaml:"execution_retention,omitempty"`
	MaxConcurrentCalls int                      `yaml:"max_concurrent_calls,omitempty"`
	Runtimes           map[string]RuntimeConfig `yaml:"runtimes"`
	Server             ServerConfig             `yaml:"server,omitempty"`
	Watchers           []WatcherConfig          `yaml:"watchers,omitempty"`
	// AllowExternalImports permits pipeline imports outside the project root.
	AllowExternalImports bool      `yaml:"allow_external_imports,omitempty"`
	MCP                  MCPConfig `yaml:"mcp,omitempty"`

	// Root is the directory containing signalbox.yaml (not serialised).
	Root string `yaml:"-"`
}

// Load reads signalbox.yaml from dir and applies defaults.
func Load(dir string) (*Config, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s found in %s (run `signalbox init` first)", FileName, dir)
		}
		return nil, err
	}
	return Parse(data, dir)
}

func Parse(data []byte, dir string) (*Config, error) {
	path := filepath.Join(dir, FileName)
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: invalid YAML: %w", path, err)
	}
	c.Root = dir
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Project == "" {
		c.Project = filepath.Base(c.Root)
	}
	if c.PipelinesDir == "" {
		c.PipelinesDir = "pipelines"
	}
	if c.StateDir == "" {
		c.StateDir = ".signalbox"
	}
	if c.ExecutionRetention == nil {
		retention := 1000
		c.ExecutionRetention = &retention
	}
	if c.MaxConcurrentCalls == 0 {
		c.MaxConcurrentCalls = DefaultMaxConcurrentCalls
	}
	if c.Server.Host == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8484
	}
}

func (c *Config) validate() error {
	if c.MaxConcurrentCalls < 1 {
		return fmt.Errorf("max_concurrent_calls must be at least 1")
	}
	if *c.ExecutionRetention < 0 {
		return fmt.Errorf("execution_retention must not be negative")
	}
	if len(c.Runtimes) == 0 {
		return fmt.Errorf("no runtimes defined")
	}
	for name, r := range c.Runtimes {
		switch r.Type {
		case "openai", "codex", "claude", "copilot", "cli", "mock":
		case "":
			return fmt.Errorf("runtime %q is missing a type", name)
		default:
			return fmt.Errorf("runtime %q has unknown type %q (want openai, codex, claude, copilot, cli, or mock)", name, r.Type)
		}
		if r.Type == "cli" && r.Command == "" {
			return fmt.Errorf("runtime %q of type cli requires a command", name)
		}
		if r.ModelArg != "" && (r.Type != "cli" || !strings.HasPrefix(r.ModelArg, "-") || strings.ContainsAny(r.ModelArg, " \t\r\n")) {
			return fmt.Errorf("runtime %q: model_arg is only for cli runtimes and must be a single flag such as --model", name)
		}
	}
	for name, server := range c.MCP.Servers {
		if !mcpNamePattern.MatchString(name) {
			return fmt.Errorf("mcp server name %q must be 1-64 letters, digits, '_' or '-'", name)
		}
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("mcp server %q is missing a command", name)
		}
		if len(server.Tools) == 0 {
			return fmt.Errorf("mcp server %q must list the tools agents may use", name)
		}
		seen := map[string]bool{}
		for _, tool := range server.Tools {
			if !mcpToolPattern.MatchString(tool) {
				return fmt.Errorf("mcp server %q tool %q must be 1-128 letters, digits, '_', '.' or '-' (wildcards are not allowed)", name, tool)
			}
			if seen[tool] {
				return fmt.Errorf("mcp server %q lists tool %q more than once", name, tool)
			}
			seen[tool] = true
		}
		for target, source := range server.EnvFrom {
			if !envNamePattern.MatchString(target) || !envNamePattern.MatchString(source) {
				return fmt.Errorf("mcp server %q env_from entries must map variable names to environment variable names", name)
			}
		}
	}
	watcherNames := map[string]bool{}
	for _, w := range c.Watchers {
		if w.Name == "" {
			return fmt.Errorf("watcher is missing a name")
		}
		if watcherNames[w.Name] {
			return fmt.Errorf("duplicate watcher name %q", w.Name)
		}
		watcherNames[w.Name] = true
		if w.Command == "" {
			return fmt.Errorf("watcher %q is missing a command", w.Name)
		}
		if w.Pipeline == "" {
			return fmt.Errorf("watcher %q is missing a pipeline", w.Name)
		}
		interval, err := time.ParseDuration(w.Interval)
		if err != nil || interval <= 0 {
			return fmt.Errorf("watcher %q has invalid interval %q (must be a positive duration such as 30s or 5m)", w.Name, w.Interval)
		}
		if w.TimeoutSeconds < 0 {
			return fmt.Errorf("watcher %q has negative timeout_seconds", w.Name)
		}
	}
	return nil
}

// PipelinesPath returns the absolute pipelines directory.
func (c *Config) PipelinesPath() string { return filepath.Join(c.Root, c.PipelinesDir) }

// StatePath returns the absolute state directory (execution history).
func (c *Config) StatePath() string { return filepath.Join(c.Root, c.StateDir) }

// Addr returns the host:port for the web server.
func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port) }
