package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type codexEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
	Item struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Text    string `json:"text"`
		Status  string `json:"status"`
		Server  string `json:"server"`
		Tool    string `json:"tool"`
		Message string `json:"message"`
	} `json:"item"`
}

// Codex JSONL contains both activity events and the final handoff payload.
// Only explicitly selected fields are logged; reasoning items are ignored.
type codexOutputWriter struct {
	pending  []byte
	onOutput func(string)
	messages map[string]string
	output   string
	err      error
}

func newCodexOutputWriter(onOutput func(string)) *codexOutputWriter {
	return &codexOutputWriter{onOutput: onOutput, messages: make(map[string]string)}
}

func (w *codexOutputWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.pending = append(w.pending, p...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		if err := w.consume(w.pending[:end]); err != nil {
			w.err = err
			return 0, err
		}
		w.pending = w.pending[end+1:]
	}
	return len(p), nil
}

func (w *codexOutputWriter) finish() error {
	if w.err != nil {
		return w.err
	}
	if err := w.consume(w.pending); err != nil {
		return err
	}
	w.pending = nil
	return nil
}

func (w *codexOutputWriter) consume(line []byte) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	var event codexEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return fmt.Errorf("invalid Codex JSON event: %w", err)
	}
	switch event.Type {
	case "thread.started":
		w.onOutput("[Codex session started]\n")
	case "turn.started":
		w.onOutput("[Codex working]\n")
	case "turn.completed":
		w.onOutput("[Codex turn completed]\n")
	case "turn.failed":
		return fmt.Errorf("Codex turn failed: %s", event.Error.Message)
	case "error":
		w.onOutput("[Codex error: " + event.Message + "]\n")
	case "item.started", "item.updated", "item.completed":
		item := event.Item
		switch item.Type {
		case "agent_message":
			prior := w.messages[item.ID]
			if strings.HasPrefix(item.Text, prior) {
				w.onOutput(strings.TrimPrefix(item.Text, prior))
			} else if item.Text != prior {
				w.onOutput("\n" + item.Text)
			}
			w.messages[item.ID] = item.Text
			if event.Type == "item.completed" {
				w.output = item.Text
				w.onOutput("\n")
			}
		case "command_execution", "mcp_tool_call", "web_search", "file_change", "todo_list":
			if event.Type == "item.updated" {
				break
			}
			label := strings.ReplaceAll(item.Type, "_", " ")
			if item.Type == "mcp_tool_call" {
				label += ": " + item.Server + "." + item.Tool
			}
			state := "started"
			if event.Type == "item.completed" {
				state = item.Status
				if state == "" {
					state = "completed"
				}
			}
			w.onOutput("[" + label + " " + state + "]\n")
		case "error":
			w.onOutput("[Codex tool error: " + item.Message + "]\n")
		}
	}
	return nil
}
