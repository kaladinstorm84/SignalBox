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

func TestConfiguredCLIOptions(t *testing.T) {
	for _, tc := range []struct {
		kind           string
		fast, standard []string
	}{
		{"codex", []string{"-c", `model_reasoning_effort="high"`, "-c", `service_tier="fast"`}, []string{"-c", `model_reasoning_effort="high"`, "-c", `service_tier="default"`}},
		{"claude", []string{"--effort", "high", "--settings", `{"fastMode":true}`}, []string{"--effort", "high", "--settings", `{"fastMode":false}`}},
		{"cli", []string{"--effort", "high", "--speed", "fast"}, []string{"--effort", "high", "--speed", "standard"}},
		{"copilot", nil, nil},
	} {
		for _, speed := range []string{"fast", "standard", ""} {
			rc := config.RuntimeConfig{Type: tc.kind, Model: "default-model", ModelArg: "--model"}
			if tc.kind != "copilot" {
				rc.ReasoningEffort, rc.Speed = "high", speed
				if tc.kind == "cli" {
					rc.Command, rc.ReasoningEffortArg, rc.SpeedArg = "bot", "--effort", "--speed"
				}
			}
			rt, err := newCLI(tc.kind, rc)
			if err != nil {
				t.Fatal(err)
			}
			c := rt.(*cliRuntime)
			for _, model := range []string{"", "override-model"} {
				args, _, cleanup, err := c.integrationArgs(Request{Model: model})
				cleanup()
				if err != nil {
					t.Fatal(err)
				}
				if model == "" {
					model = "default-model"
				}
				want := []string{"--model", model}
				if tc.kind != "copilot" {
					options := tc.fast
					if speed == "standard" {
						options = tc.standard
					} else if speed == "" {
						options = tc.fast[:2]
					}
					want = append(want, options...)
				}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("%s speed=%q args = %q, want %q", tc.kind, speed, args, want)
				}
			}
		}
	}
	if _, err := NewRegistry(map[string]config.RuntimeConfig{"bad": {Type: "copilot", Speed: "fast"}}); err == nil {
		t.Fatal("runtime builder accepted an unsupported option")
	}
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

func TestIntegrationArgsHTTP(t *testing.T) {
	t.Setenv("SBX_HTTP_AUTH", "Bearer secret")
	servers := map[string]config.MCPServerConfig{
		"remote": {URL: "https://example.com/mcp", HeadersFrom: map[string]string{"Authorization": "SBX_HTTP_AUTH"}, Tools: []string{"search", "write"}},
		"local":  {Command: "local", EnvFrom: map[string]string{"SIGNALBOX_MCP_HEADER_0": "SBX_HTTP_AUTH"}, Tools: []string{"read"}},
	}
	grants := []MCPGrant{{Server: "remote", Tools: []string{"search"}}, {Server: "local", Tools: []string{"read"}}}
	for _, kind := range []string{"codex", "claude", "copilot"} {
		t.Run(kind, func(t *testing.T) {
			c := &cliRuntime{name: kind, kind: kind, mcpServers: servers}
			args, env, cleanup, err := c.integrationArgs(Request{MCP: grants})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if strings.Contains(strings.Join(args, " "), "Bearer secret") {
				t.Fatal("secret leaked into arguments")
			}
			if kind == "codex" {
				for _, want := range []string{`mcp_servers.remote.url="https://example.com/mcp"`, `mcp_servers.remote.env_http_headers."Authorization"="SIGNALBOX_MCP_HEADER_1"`, `mcp_servers.remote.enabled_tools=["search"]`, `mcp_servers.local.command="local"`} {
					if !strings.Contains(strings.Join(args, " "), want) {
						t.Errorf("missing %s in %q", want, args)
					}
				}
				if !reflect.DeepEqual(env, []string{"SIGNALBOX_MCP_HEADER_1=Bearer secret", "SIGNALBOX_MCP_HEADER_0=Bearer secret"}) {
					t.Fatalf("environment = %q", env)
				}
				return
			}
			var path string
			for i, arg := range args {
				if arg == "--mcp-config" || arg == "--additional-mcp-config" {
					path = strings.TrimPrefix(args[i+1], "@")
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Servers map[string]map[string]any `json:"mcpServers"`
			}
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			remote := cfg.Servers["remote"]
			headers, ok := remote["headers"].(map[string]any)
			if !ok || headers["Authorization"] != "Bearer secret" || remote["type"] != "http" || remote["url"] != "https://example.com/mcp" {
				t.Fatalf("remote config = %s", data)
			}
			if _, ok := remote["command"]; ok {
				t.Fatalf("HTTP server contains command: %s", data)
			}
			if kind == "copilot" && !reflect.DeepEqual(remote["tools"], []any{"search"}) {
				t.Fatalf("tool grant = %v", remote["tools"])
			}
			if kind == "claude" && !strings.Contains(strings.Join(args, " "), "mcp__remote__search") {
				t.Fatalf("missing allowed tool: %q", args)
			}
		})
	}
}

func TestHTTPMCPHeaderErrors(t *testing.T) {
	servers := map[string]config.MCPServerConfig{"remote": {URL: "https://example.com/mcp", HeadersFrom: map[string]string{"Authorization": "SBX_MISSING_HTTP_AUTH"}}}
	c := &cliRuntime{name: "codex", kind: "codex", mcpServers: servers}
	t.Setenv("SBX_MISSING_HTTP_AUTH", "invalid\r\nheader")
	if _, _, _, err := c.integrationArgs(Request{MCP: []MCPGrant{{Server: "remote"}}}); err == nil || !strings.Contains(err.Error(), "invalid characters") {
		t.Fatalf("invalid header error = %v", err)
	}
	if err := os.Unsetenv("SBX_MISSING_HTTP_AUTH"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.integrationArgs(Request{MCP: []MCPGrant{{Server: "remote"}}}); err == nil || !strings.Contains(err.Error(), "SBX_MISSING_HTTP_AUTH") {
		t.Fatalf("missing header error = %v", err)
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
