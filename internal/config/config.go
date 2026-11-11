// Package config loads the project-level signalbox.yaml file.
package config

import (
	"fmt"
	"net/url"
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

	// Model is the default for every call; pipeline model overrides take precedence.
	Model           string `yaml:"model,omitempty"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty"`
	// Speed is standard or fast (provider priority service, potentially higher cost).
	Speed string `yaml:"speed,omitempty"`

	// --- openai ---
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
	// Generic CLI flags receive the configured values as separate arguments.
	ReasoningEffortArg string `yaml:"reasoning_effort_arg,omitempty"`
	SpeedArg           string `yaml:"speed_arg,omitempty"`
}

// MCPConfig registers MCP servers agents may opt in to. Nothing is passed to
// an agent CLI unless Enabled is true and the agent lists the server.
type MCPConfig struct {
	Enabled bool                       `yaml:"enabled,omitempty"`
	Servers map[string]MCPServerConfig `yaml:"servers,omitempty"`
}

// MCPServerConfig describes either a stdio command or an HTTP MCP endpoint.
// EnvFrom and HeadersFrom refer to SignalBox process environment variables.
type MCPServerConfig struct {
	Command     string            `yaml:"command,omitempty"`
	Args        []string          `yaml:"args,omitempty"`
	EnvFrom     map[string]string `yaml:"env_from,omitempty"`
	URL         string            `yaml:"url,omitempty"`
	HeadersFrom map[string]string `yaml:"headers_from,omitempty"`
	// Tools is the allow-list of tools agents may be granted.
	Tools []string `yaml:"tools"`
}

var (
	mcpNamePattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	mcpToolPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	envNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
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
	Version      int    `yaml:"version"`
	Project      string `yaml:"project"`
	PipelinesDir string `yaml:"pipelines_dir,omitempty"`
	// SchemasDir holds JSON Schema files for structured output (default
	// schemas). It must stay inside the project root.
	SchemasDir         string                   `yaml:"schemas_dir,omitempty"`
	StateDir           string                   `yaml:"state_dir,omitempty"`
	ExecutionRetention *int                     `yaml:"execution_retention,omitempty"`
	MaxConcurrentCalls int                      `yaml:"max_concurrent_calls,omitempty"`
	Runtimes           map[string]RuntimeConfig `yaml:"runtimes"`
	Server             ServerConfig             `yaml:"server,omitempty"`
	Watchers           []WatcherConfig          `yaml:"watchers,omitempty"`
	// AllowExternalImports permits pipeline imports outside the project root.
	AllowExternalImports bool      `yaml:"allow_external_imports,omitempty"`
	MCP                  MCPConfig `yaml:"mcp,omitempty"`
	// Registry is the shared pipeline registry used by `signalbox registry`.
	Registry RegistryConfig `yaml:"registry,omitempty"`
	// Uploads limits the files attached to executions.
	Uploads UploadsConfig `yaml:"uploads,omitempty"`

	// Root is the directory containing signalbox.yaml (not serialised).
	Root string `yaml:"-"`
}

// RegistryConfig locates a directory (or file share) pipeline registry.
type RegistryConfig struct {
	// Path is absolute, relative to the project root, or a file:// URL.
	Path string `yaml:"path,omitempty"`
}

// Upload limit defaults.
const (
	DefaultUploadMaxBytes = 10 << 20
	DefaultUploadMaxFiles = 10
	// MaxUploadBytes caps uploads.max_bytes.
	MaxUploadBytes = 1 << 30
	// MaxUploadFiles caps uploads.max_files.
	MaxUploadFiles = 100
)

// DefaultUploadExtensions are the file types accepted when
// uploads.allowed_extensions is not set: text, data, documents, and images.
var DefaultUploadExtensions = []string{
	".txt", ".md", ".csv", ".tsv", ".json", ".yaml", ".yml", ".xml", ".log",
	".pdf", ".docx", ".xlsx", ".pptx",
	".png", ".jpg", ".jpeg", ".gif", ".webp",
}

// UploadsConfig bounds the files attached to one execution.
type UploadsConfig struct {
	// MaxBytes is the largest single file accepted (default 10 MiB).
	MaxBytes int64 `yaml:"max_bytes,omitempty"`
	// MaxFiles is the most files one execution may carry (default 10).
	MaxFiles int `yaml:"max_files,omitempty"`
	// AllowedExtensions lists accepted extensions (default: common text,
	// data, document, and image types).
	AllowedExtensions []string `yaml:"allowed_extensions,omitempty"`
}

var uploadExtPattern = regexp.MustCompile(`^\.[a-z0-9][a-z0-9_+-]{0,15}$`)

// RegistryPath resolves the configured registry directory, or "" when none
// is configured.
func (c *Config) RegistryPath() string {
	return ResolveRegistryPath(c.Root, c.Registry.Path)
}

// ResolveRegistryPath turns a registry location into a directory path.
func ResolveRegistryPath(root, location string) string {
	location = strings.TrimSpace(location)
	if location == "" {
		return ""
	}
	if strings.HasPrefix(location, "file://") {
		location = strings.TrimPrefix(location, "file://")
		// file:///C:/share -> C:/share on Windows.
		if len(location) > 2 && location[0] == '/' && location[2] == ':' {
			location = location[1:]
		}
		return filepath.Clean(filepath.FromSlash(location))
	}
	if filepath.IsAbs(location) {
		return filepath.Clean(location)
	}
	return filepath.Join(root, location)
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

// applyDefaults runs before validation. A nil retention selects the default;
// an explicit zero must survive because it means retain history indefinitely.
func (c *Config) applyDefaults() {
	if c.Project == "" {
		c.Project = filepath.Base(c.Root)
	}
	if c.PipelinesDir == "" {
		c.PipelinesDir = "pipelines"
	}
	if c.SchemasDir == "" {
		c.SchemasDir = "schemas"
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
	if c.Uploads.MaxBytes == 0 {
		c.Uploads.MaxBytes = DefaultUploadMaxBytes
	}
	if c.Uploads.MaxFiles == 0 {
		c.Uploads.MaxFiles = DefaultUploadMaxFiles
	}
	if len(c.Uploads.AllowedExtensions) == 0 {
		c.Uploads.AllowedExtensions = append([]string(nil), DefaultUploadExtensions...)
	}
	for i, ext := range c.Uploads.AllowedExtensions {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext != "" && !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		c.Uploads.AllowedExtensions[i] = ext
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
	if rel := filepath.Clean(filepath.FromSlash(c.SchemasDir)); filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("schemas_dir %q must be a relative path inside the project root", c.SchemasDir)
	}
	if c.Uploads.MaxBytes < 1 || c.Uploads.MaxBytes > MaxUploadBytes {
		return fmt.Errorf("uploads.max_bytes must be between 1 and %d", int64(MaxUploadBytes))
	}
	if c.Uploads.MaxFiles < 1 || c.Uploads.MaxFiles > MaxUploadFiles {
		return fmt.Errorf("uploads.max_files must be between 1 and %d", MaxUploadFiles)
	}
	for _, ext := range c.Uploads.AllowedExtensions {
		if !uploadExtPattern.MatchString(ext) {
			return fmt.Errorf("uploads.allowed_extensions: %q is not a file extension such as .csv", ext)
		}
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
		if err := ValidateRuntimeOptions(r); err != nil {
			return fmt.Errorf("runtime %q: %w", name, err)
		}
	}

	for name, server := range c.MCP.Servers {
		if !mcpNamePattern.MatchString(name) {
			return fmt.Errorf("mcp server name %q must be 1-64 letters, digits, '_' or '-'", name)
		}
		if server.URL == "" {
			if strings.TrimSpace(server.Command) == "" {
				return fmt.Errorf("mcp server %q is missing a command or URL", name)
			}
			if len(server.HeadersFrom) != 0 {
				return fmt.Errorf("mcp server %q headers_from requires a URL", name)
			}
		} else {
			u, err := url.Parse(server.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("mcp server %q URL must be an HTTP(S) endpoint without credentials or a fragment", name)
			}
			if server.Command != "" || len(server.Args) != 0 || len(server.EnvFrom) != 0 {
				return fmt.Errorf("mcp server %q URL cannot be combined with command, args, or env_from", name)
			}
		}
		headers := map[string]bool{}
		for header, source := range server.HeadersFrom {
			key := strings.ToLower(header)
			if !headerNamePattern.MatchString(header) || !envNamePattern.MatchString(source) {
				return fmt.Errorf("mcp server %q headers_from must map valid HTTP header names to environment variable names", name)
			}
			if headers[key] {
				return fmt.Errorf("mcp server %q lists HTTP header %q more than once", name, header)
			}
			headers[key] = true
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

// SchemasPath returns the schemas directory.
func (c *Config) SchemasPath() string { return filepath.Join(c.Root, filepath.FromSlash(c.SchemasDir)) }

// StatePath returns the absolute state directory (execution history).
func (c *Config) StatePath() string { return filepath.Join(c.Root, c.StateDir) }

// Addr returns the host:port for the web server.
func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port) }

// ValidateRuntimeOptions rejects settings that a runtime cannot pass through.
func ValidateRuntimeOptions(r RuntimeConfig) error {
	if r.Model != "" && (strings.HasPrefix(r.Model, "-") || strings.ContainsAny(r.Model, " \t\r\n")) {
		return fmt.Errorf("model must be a single model name, not flags or whitespace")
	}
	if r.Type == "cli" && r.Model != "" && r.ModelArg == "" {
		return fmt.Errorf("model requires model_arg for a cli runtime")
	}
	for field, flag := range map[string]string{"reasoning_effort_arg": r.ReasoningEffortArg, "speed_arg": r.SpeedArg} {
		if flag != "" && (r.Type != "cli" || !strings.HasPrefix(flag, "-") || strings.ContainsAny(flag, " \t\r\n")) {
			return fmt.Errorf("%s is only for cli runtimes and must be a single flag", field)
		}
	}
	if r.Speed != "" && r.Speed != "standard" && r.Speed != "fast" {
		return fmt.Errorf("speed must be standard or fast")
	}
	if effort := r.ReasoningEffort; effort != "" {
		if strings.HasPrefix(effort, "-") || strings.ContainsAny(effort, " \t\r\n") {
			return fmt.Errorf("reasoning_effort must be a single value")
		}
		levels := ""
		switch r.Type {
		case "openai":
			levels = " none minimal low medium high xhigh "
		case "codex":
			levels = " none minimal low medium high xhigh max ultra "
		case "claude":
			levels = " low medium high xhigh max ultracode "
		case "cli":
			if r.ReasoningEffortArg == "" {
				return fmt.Errorf("reasoning_effort requires reasoning_effort_arg for a cli runtime")
			}
			levels = " " + effort + " "
		default:
			return fmt.Errorf("reasoning_effort is not supported for runtime type %s", r.Type)
		}
		if !strings.Contains(levels, " "+effort+" ") {
			return fmt.Errorf("unsupported reasoning_effort %q for runtime type %s", effort, r.Type)
		}
	}
	if r.Speed != "" {
		switch r.Type {
		case "openai", "codex", "claude":
		case "cli":
			if r.SpeedArg == "" {
				return fmt.Errorf("speed requires speed_arg for a cli runtime")
			}
		default:
			return fmt.Errorf("speed is not supported for runtime type %s", r.Type)
		}
	}
	return nil
}
