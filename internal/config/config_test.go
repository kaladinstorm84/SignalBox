package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestRuntimeCallOptions(t *testing.T) {
	for _, kind := range []string{"openai", "codex", "claude", "cli"} {
		settings := "model: test-model\n    reasoning_effort: high\n    speed: fast\n"
		if kind == "cli" {
			settings += "    command: bot\n    model_arg: --model\n    reasoning_effort_arg: --effort\n    speed_arg: --speed\n"
		}
		cfg, err := Parse([]byte(fmt.Sprintf("version: 1\nruntimes:\n  ai:\n    type: %s\n    %s", kind, settings)), ".")
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Runtimes["ai"]; got.Model != "test-model" || got.ReasoningEffort != "high" || got.Speed != "fast" {
			t.Fatalf("parsed settings = %+v", got)
		}
	}
	for _, settings := range []string{
		"type: codex\n    speed: turbo",
		"type: codex\n    reasoning_effort: 'low medium'",
		"type: claude\n    reasoning_effort: minimal",
		"type: openai\n    reasoning_effort: max",
		"type: copilot\n    speed: fast",
		"type: copilot\n    reasoning_effort: high",
		"type: mock\n    speed: standard",
		"type: cli\n    command: bot\n    model: default",
		"type: cli\n    command: bot\n    reasoning_effort: high",
		"type: cli\n    command: bot\n    speed: fast",
		"type: cli\n    command: bot\n    speed_arg: '--speed extra'",
		"type: codex\n    reasoning_effort_arg: --effort",
		"type: codex\n    model: '--bad flag'",
	} {
		if _, err := Parse([]byte("version: 1\nruntimes:\n  ai:\n    "+settings+"\n"), "."); err == nil {
			t.Fatalf("accepted invalid settings: %s", settings)
		}
	}
}

func TestExecutionRetentionDefaultAndValidation(t *testing.T) {
	base := "version: 1\nproject: test\nruntimes:\n  mock:\n    type: mock\n"
	cfg, err := Parse([]byte(base), ".")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutionRetention == nil || *cfg.ExecutionRetention != 1000 {
		t.Fatalf("default execution retention = %v, want 1000", cfg.ExecutionRetention)
	}

	cfg, err = Parse([]byte(base+"execution_retention: 0\n"), ".")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutionRetention == nil || *cfg.ExecutionRetention != 0 {
		t.Fatalf("explicit unlimited retention = %v, want 0", cfg.ExecutionRetention)
	}

	_, err = Parse([]byte(base+"execution_retention: -1\n"), ".")
	if err == nil || !strings.Contains(err.Error(), "execution_retention must not be negative") {
		t.Fatalf("negative retention error = %v", err)
	}
}

func TestModelArgAndMCPValidation(t *testing.T) {
	base := "version: 1\nproject: test\nruntimes:\n  mock:\n    type: mock\n"
	good := base + "  bot:\n    type: cli\n    command: bot\n    model_arg: --model\n" +
		"allow_external_imports: true\nmcp:\n  enabled: true\n  servers:\n    gh:\n      command: gh-mcp\n      args: [--stdio]\n      env_from: {GH_TOKEN: MY_TOKEN}\n      tools: [search, read]\n"
	cfg, err := Parse([]byte(good), ".")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtimes["bot"].ModelArg != "--model" || !cfg.AllowExternalImports || !cfg.MCP.Enabled || cfg.MCP.Servers["gh"].EnvFrom["GH_TOKEN"] != "MY_TOKEN" {
		t.Fatalf("parsed = %+v", cfg)
	}
	bad := map[string]string{
		"model_arg on mock":  base + "  m2:\n    type: mock\n    model_arg: --model\n",
		"model_arg not flag": base + "  bot:\n    type: cli\n    command: bot\n    model_arg: model\n",
		"server name":        base + "mcp:\n  servers:\n    'bad name':\n      command: x\n      tools: [a]\n",
		"missing command":    base + "mcp:\n  servers:\n    gh:\n      tools: [a]\n",
		"missing tools":      base + "mcp:\n  servers:\n    gh:\n      command: x\n",
		"wildcard tool":      base + "mcp:\n  servers:\n    gh:\n      command: x\n      tools: ['*']\n",
		"bad env name":       base + "mcp:\n  servers:\n    gh:\n      command: x\n      tools: [a]\n      env_from: {'A B': X}\n",
	}
	for name, src := range bad {
		if _, err := Parse([]byte(src), "."); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestUploadsDefaultsAndValidation(t *testing.T) {
	base := "version: 1\nproject: test\nruntimes:\n  mock:\n    type: mock\n"
	cfg, err := Parse([]byte(base), ".")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Uploads.MaxBytes != DefaultUploadMaxBytes || cfg.Uploads.MaxFiles != DefaultUploadMaxFiles || len(cfg.Uploads.AllowedExtensions) != len(DefaultUploadExtensions) {
		t.Fatalf("defaults = %+v", cfg.Uploads)
	}
	cfg, err = Parse([]byte(base+"uploads:\n  max_bytes: 1024\n  max_files: 3\n  allowed_extensions: [CSV, .Md]\n"), ".")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Uploads.MaxBytes != 1024 || cfg.Uploads.MaxFiles != 3 || strings.Join(cfg.Uploads.AllowedExtensions, " ") != ".csv .md" {
		t.Fatalf("configured = %+v", cfg.Uploads)
	}
	for _, bad := range []string{
		"uploads:\n  max_bytes: -1\n",
		"uploads:\n  max_bytes: 2000000000000\n",
		"uploads:\n  max_files: 1000\n",
		"uploads:\n  allowed_extensions: [\"../x\"]\n",
	} {
		if _, err := Parse([]byte(base+bad), "."); err == nil || !strings.Contains(err.Error(), "uploads") {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
}
