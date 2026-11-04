// Package config loads the project-level signalbox.yaml file.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FileName is the project configuration file created by `signalbox init`.
const FileName = "signalbox.yaml"

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
}

// ServerConfig controls `signalbox serve`.
type ServerConfig struct {
	Host string `yaml:"host,omitempty"`
	Port int    `yaml:"port,omitempty"`
}

// Config is the parsed signalbox.yaml.
type Config struct {
	Version      int                      `yaml:"version"`
	Project      string                   `yaml:"project"`
	PipelinesDir string                   `yaml:"pipelines_dir,omitempty"`
	StateDir     string                   `yaml:"state_dir,omitempty"`
	Runtimes     map[string]RuntimeConfig `yaml:"runtimes"`
	Server       ServerConfig             `yaml:"server,omitempty"`

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
	if c.Server.Host == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8484
	}
}

func (c *Config) validate() error {
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
	}
	return nil
}

// PipelinesPath returns the absolute pipelines directory.
func (c *Config) PipelinesPath() string { return filepath.Join(c.Root, c.PipelinesDir) }

// StatePath returns the absolute state directory (execution history).
func (c *Config) StatePath() string { return filepath.Join(c.Root, c.StateDir) }

// Addr returns the host:port for the web server.
func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port) }
