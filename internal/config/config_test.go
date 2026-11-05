package config

import (
	"strings"
	"testing"
)

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
