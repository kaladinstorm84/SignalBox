package watcher

import (
	"context"
	"reflect"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

func TestPollStartsPipelineOnlyForChangedOutput(t *testing.T) {
	stateDir := t.TempDir()
	var started []string
	cfg := config.WatcherConfig{Name: "jira", Pipeline: "triage"}
	runner := New(stateDir, func(_, input string) (string, error) {
		started = append(started, input)
		return "exec", nil
	})
	outputs := []string{"issue 1", "issue 1", "issue 2"}
	runner.poll = func(context.Context, config.WatcherConfig) (string, error) {
		output := outputs[0]
		outputs = outputs[1:]
		return output, nil
	}
	for range 3 {
		if err := runner.pollOnce(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(started, []string{"issue 1", "issue 2"}) {
		t.Fatalf("started inputs = %v", started)
	}

	// The unchanged output remains suppressed after a server restart.
	restarted := New(stateDir, func(_, input string) (string, error) {
		started = append(started, input)
		return "exec", nil
	})
	restarted.poll = func(context.Context, config.WatcherConfig) (string, error) {
		return "issue 2", nil
	}
	if err := restarted.pollOnce(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(started) != 2 {
		t.Fatalf("unchanged output started another execution: %v", started)
	}

	// Returning to prior content after an empty poll is a new change.
	restarted.poll = func(context.Context, config.WatcherConfig) (string, error) {
		return "", nil
	}
	if err := restarted.pollOnce(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	restarted.poll = func(context.Context, config.WatcherConfig) (string, error) {
		return "issue 2", nil
	}
	if err := restarted.pollOnce(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(started, []string{"issue 1", "issue 2", "issue 2"}) {
		t.Fatalf("started inputs after content reappeared = %v", started)
	}
}
