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
