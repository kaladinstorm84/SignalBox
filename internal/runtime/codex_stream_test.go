package runtime

import (
	"strings"
	"testing"
)

func TestCodexEventChunksAndMessageUpdates(t *testing.T) {
	var log strings.Builder
	writer := newCodexOutputWriter(func(text string) { log.WriteString(text) })
	events := "{\"type\":\"item.updated\",\"item\":{\"id\":\"m\",\"type\":\"agent_message\",\"text\":\"Hello\"}}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"id\":\"m\",\"type\":\"agent_message\",\"text\":\"Hello world\"}}"
	for i := 0; i < len(events); i += 7 {
		end := i + 7
		if end > len(events) {
			end = len(events)
		}
		if _, err := writer.Write([]byte(events[i:end])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.finish(); err != nil {
		t.Fatal(err)
	}
	if writer.output != "Hello world" || log.String() != "Hello world\n" {
		t.Fatalf("output = %q, log = %q", writer.output, log.String())
	}
}

func TestCodexEventErrorsAreNotSuccessfulOutput(t *testing.T) {
	for _, line := range []string{
		"not JSON\n",
		"{\"type\":\"turn.failed\",\"error\":{\"message\":\"provider unavailable\"}}\n",
	} {
		writer := newCodexOutputWriter(func(string) {})
		if _, err := writer.Write([]byte(line)); err == nil {
			t.Fatalf("invalid or failed event accepted: %q", line)
		}
		if err := writer.finish(); err == nil {
			t.Fatal("event error was lost")
		}
	}
}
