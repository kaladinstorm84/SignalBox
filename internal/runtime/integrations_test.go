package runtime

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

var testServers = map[string]config.MCPServerConfig{
	"gh": {Command: "gh-mcp", Args: []string{"--stdio"}, Tools: []string{"search", "read"}, EnvFrom: map[string]string{"GH_TOKEN": "SBX_TEST_GH_TOKEN"}},
}

func TestIntegrationArgsCodex(t *testing.T) {
	t.Setenv("SBX_TEST_GH_TOKEN", "s3cret")
	c := &cliRuntime{name: "codex", kind: "codex", modelArg: "--model", args: []string{"exec", "-"}, mcpServers: testServers}
	args, env, cleanup, err := c.integrationArgs(Request{Model: "o4", MCP: []MCPGrant{{Server: "gh", Tools: []string{"search"}}}})
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "o4",
		"-c", `mcp_servers.gh.command="gh-mcp"`,
		"-c", `mcp_servers.gh.args=["--stdio"]`,
		"-c", `mcp_servers.gh.enabled_tools=["search"]`,
		"-c", `mcp_servers.gh.env_vars=["GH_TOKEN"]`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %q", args)
	}
	if strings.Contains(strings.Join(args, " "), "s3cret") {
		t.Fatal("secret leaked into argv")
	}
	if !reflect.DeepEqual(env, []string{"GH_TOKEN=s3cret"}) {
		t.Fatalf("env = %q", env)
	}
	if got := c.baseArgs(args); got[0] != "exec" || got[2] != "--model" {
		t.Fatalf("codex flags should follow the subcommand: %q", got)
	}
}

func TestIntegrationArgsClaude(t *testing.T) {
	t.Setenv("SBX_TEST_GH_TOKEN", "s3cret")
	c := &cliRuntime{name: "claude", kind: "claude", modelArg: "--model", args: []string{"-p"}, mcpServers: testServers}
	args, env, cleanup, err := c.integrationArgs(Request{MCP: []MCPGrant{{Server: "gh", Tools: []string{"search", "read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("env = %q", env)
	}
	if len(args) != 5 || args[0] != "--allowedTools" || args[1] != "mcp__gh__search,mcp__gh__read" || args[2] != "--mcp-config" || args[4] != "--strict-mcp-config" {
		t.Fatalf("args = %q", args)
	}
	path := args[3]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type, Command string
			Args          []string
			Env           map[string]string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	gh := cfg.MCPServers["gh"]
	if gh.Type != "stdio" || gh.Command != "gh-mcp" || gh.Env["GH_TOKEN"] != "s3cret" {
		t.Fatalf("config = %s", data)
	}
	if got := c.baseArgs(args); got[len(got)-1] != "-p" {
		t.Fatalf("claude flags should precede -p: %q", got)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp config not removed: %v", err)
	}
}

func TestIntegrationArgsCopilot(t *testing.T) {
	t.Setenv("SBX_TEST_GH_TOKEN", "s3cret")
	c := &cliRuntime{name: "copilot", kind: "copilot", modelArg: "--model", args: []string{"-p"}, mcpServers: testServers}
	args, _, cleanup, err := c.integrationArgs(Request{Model: "gpt-5", MCP: []MCPGrant{{Server: "gh", Tools: []string{"read"}}}})
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 6 || args[0] != "--model" || args[2] != "--additional-mcp-config" || !strings.HasPrefix(args[3], "@") || args[4] != "--allow-tool" || args[5] != "gh(read)" {
		t.Fatalf("args = %q", args)
	}
	data, err := os.ReadFile(strings.TrimPrefix(args[3], "@"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"local"`) || !strings.Contains(string(data), `"tools":["read"]`) {
		t.Fatalf("config = %s", data)
	}
	if got := c.baseArgs(args); got[len(got)-1] != "-p" || got[0] != "--model" {
		t.Fatalf("copilot flags should precede -p: %q", got)
	}
}

func TestIntegrationArgsErrors(t *testing.T) {
	os.Unsetenv("SBX_TEST_GH_TOKEN")
	codex := &cliRuntime{name: "codex", kind: "codex", modelArg: "--model", mcpServers: testServers}
	if _, _, _, err := codex.integrationArgs(Request{MCP: []MCPGrant{{Server: "gh", Tools: []string{"search"}}}}); err == nil || !strings.Contains(err.Error(), "SBX_TEST_GH_TOKEN") {
		t.Fatalf("missing env: %v", err)
	}
	generic := &cliRuntime{name: "bot", kind: "cli"}
	if _, _, _, err := generic.integrationArgs(Request{Model: "m"}); err == nil || !strings.Contains(err.Error(), "model_arg") {
		t.Fatalf("cli without model_arg: %v", err)
	}
	if _, _, _, err := generic.integrationArgs(Request{MCP: []MCPGrant{{Server: "gh"}}}); err == nil {
		t.Fatal("expected MCP error for generic cli")
	}
	if _, _, _, err := codex.integrationArgs(Request{Model: "--danger"}); err == nil {
		t.Fatal("expected invalid model error")
	}
	withArg := &cliRuntime{name: "bot", kind: "cli", modelArg: "-m", args: []string{"run"}}
	args, _, _, err := withArg.integrationArgs(Request{Model: "m1"})
	if err != nil || !reflect.DeepEqual(withArg.baseArgs(args), []string{"run", "-m", "m1"}) {
		t.Fatalf("cli model_arg: %q %v", args, err)
	}
}

func TestValidatePipeline(t *testing.T) {
	cfg := &config.Config{
		Runtimes: map[string]config.RuntimeConfig{
			"codex": {Type: "codex"},
			"bot":   {Type: "cli", Command: "bot"},
			"mock":  {Type: "mock"},
		},
		MCP: config.MCPConfig{Enabled: true, Servers: testServers},
	}
	cases := map[string]string{
		"references runtime": "name: p\nentry: a\nagents:\n  - id: a\n    runtime: nope\n",
		"no model_arg":       "name: p\nentry: a\nagents:\n  - id: a\n    runtime: bot\n    model: m\n",
		"MCP needs":          "name: p\nentry: a\nagents:\n  - id: a\n    runtime: mock\n    mcp: [gh]\n",
		"not registered":     "name: p\nentry: a\nagents:\n  - id: a\n    runtime: codex\n    mcp: [other]\n",
		"does not allow":     "name: p\nentry: a\nagents:\n  - id: a\n    runtime: codex\n    mcp: [{server: gh, tools: [write]}]\n",
	}
	for want, src := range cases {
		p, err := pipeline.Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: parse: %v", want, err)
		}
		if err := ValidatePipeline(cfg, p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	ok, _ := pipeline.Parse([]byte("name: p\nentry: a\nagents:\n  - id: a\n    runtime: codex\n    model: o4\n    mcp: [gh]\n"))
	if err := ValidatePipeline(cfg, ok); err != nil {
		t.Fatalf("valid pipeline: %v", err)
	}
	cfg.MCP.Enabled = false
	if err := ValidatePipeline(cfg, ok); err == nil || !strings.Contains(err.Error(), "mcp.enabled") {
		t.Fatalf("disabled: %v", err)
	}
}

func TestValidateProjectWatcherParams(t *testing.T) {
	p, err := pipeline.Parse([]byte("name: p\nparams:\n  topic: {required: true}\nentry: a\nagents:\n  - id: a\n    runtime: mock\n    prompt: ${{ params.topic }}\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{"mock": {Type: "mock"}},
		Watchers: []config.WatcherConfig{{Name: "w", Pipeline: "p"}}}
	if err := ValidateProject(cfg, map[string]*pipeline.Pipeline{"p": p}); err == nil || !strings.Contains(err.Error(), "topic") {
		t.Fatalf("watcher required param: %v", err)
	}
	cfg.Watchers[0].Pipeline = "missing"
	if err := ValidateProject(cfg, map[string]*pipeline.Pipeline{"p": p}); err == nil || !strings.Contains(err.Error(), "unknown pipeline") {
		t.Fatalf("unknown pipeline: %v", err)
	}
}

func TestValidateUploadDelivery(t *testing.T) {
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{"api": {Type: "openai"}, "mock": {Type: "mock"}},
		Watchers: []config.WatcherConfig{{Name: "w", Pipeline: "p"}}}
	byPath, _ := pipeline.Parse([]byte("name: p\nagents:\n  - id: a\n    runtime: api\n    files: path\n"))
	if err := ValidatePipeline(cfg, byPath); err == nil || !strings.Contains(err.Error(), "files: path") {
		t.Fatalf("openai path delivery: %v", err)
	}
	inline, _ := pipeline.Parse([]byte("name: p\nagents:\n  - id: a\n    runtime: api\n    files: inline\n"))
	if err := ValidatePipeline(cfg, inline); err != nil {
		t.Fatalf("openai inline delivery: %v", err)
	}
	required, _ := pipeline.Parse([]byte("name: p\nfiles: {required: true}\nagents:\n  - id: a\n    runtime: mock\n"))
	if err := ValidateProject(cfg, map[string]*pipeline.Pipeline{"p": required}); err == nil || !strings.Contains(err.Error(), "files") {
		t.Fatalf("watcher with required files: %v", err)
	}
}
