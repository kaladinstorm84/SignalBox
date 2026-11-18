package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ClientVersion identifies SignalBox to the Codex App Server.
var ClientVersion = "dev"

// appServerGrace bounds how long an interrupted turn may take to stop, and
// how long the server may take to exit after stdin closes, before it is killed.
var appServerGrace = 5 * time.Second

// appServerOptOut suppresses high-volume notifications SignalBox never logs.
var appServerOptOut = []string{
	"item/reasoning/textDelta",
	"item/reasoning/summaryTextDelta",
	"item/reasoning/summaryPartAdded",
	"item/commandExecution/outputDelta",
	"item/fileChange/outputDelta",
	"command/exec/outputDelta",
	"thread/tokenUsage/updated",
	"account/rateLimits/updated",
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type appServerItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Text   string `json:"text"`
	Phase  string `json:"phase"`
	Status string `json:"status"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
}

type appServerTurn struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Items  []appServerItem `json:"items"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// appServerCall is one SignalBox request against a dedicated app-server
// process: a single thread with a single turn. Only the calling goroutine
// writes to stdin.
type appServerCall struct {
	stdin    io.Writer
	messages <-chan rpcMessage
	readErr  <-chan error
	nextID   int
	onOutput func(string)

	threadID string
	turnID   string
	// streamed marks agent messages whose text already arrived as deltas.
	streamed map[string]bool
	final    string
	last     string
	done     bool
	turn     appServerTurn
}

// invokeAppServer runs the prompt as one turn of `codex app-server` over
// stdio. Agent text streams as it is generated; tool activity is logged as
// markers; reasoning is never requested or logged.
func (c *cliRuntime) invokeAppServer(ctx context.Context, prompt string, args, env []string, timeout time.Duration, onOutput func(string)) (string, error) {
	if onOutput == nil {
		onOutput = func(string) {}
	}
	procCtx, kill := context.WithCancel(context.Background())
	defer kill()
	cmd := exec.CommandContext(procCtx, c.command, args...)
	configureProcess(cmd)
	cmd.Dir = c.dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.WaitDelay = appServerGrace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr := &tailBuffer{limit: 16 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s failed: %w", c.command, err)
	}

	stop := make(chan struct{})
	messages := make(chan rpcMessage, 64)
	readErr := make(chan error, 1)
	go readAppServer(stdout, messages, readErr, stop)
	// A blocked stdin write cannot observe ctx, so the process is killed if
	// it has not stopped within the grace period after cancellation.
	go func() {
		select {
		case <-ctx.Done():
			select {
			case <-time.After(appServerGrace):
				kill()
			case <-stop:
			}
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		_ = stdin.Close()
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		select {
		case <-exited:
		case <-time.After(appServerGrace):
			kill()
			<-exited
		}
	}()

	call := &appServerCall{stdin: stdin, messages: messages, readErr: readErr, onOutput: onOutput, streamed: map[string]bool{}}
	fail := func(err error) (string, error) {
		if ctx.Err() != nil {
			call.interrupt()
			if ctx.Err() == context.DeadlineExceeded {
				return "", fmt.Errorf("%s timed out after %s", c.command, timeout)
			}
			return "", fmt.Errorf("%s was cancelled: %w", c.command, ctx.Err())
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("%w: %s", err, truncate(detail, 800))
		}
		return "", err
	}

	if _, err := call.request(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "signalbox", "title": "SignalBox", "version": ClientVersion},
		"capabilities": map[string]any{"optOutNotificationMethods": appServerOptOut},
	}); err != nil {
		return fail(fmt.Errorf("Codex app-server initialize: %w", err))
	}
	if err := call.send(map[string]string{"method": "initialized"}); err != nil {
		return fail(err)
	}
	// SignalBox cannot answer approval prompts mid-call, so it asks for the
	// same non-interactive policy `codex exec` uses. Sandboxing still follows
	// the user's Codex configuration.
	threadParams := map[string]any{"approvalPolicy": "never"}
	if c.dir != "" {
		threadParams["cwd"] = c.dir
	}
	result, err := call.request(ctx, "thread/start", threadParams)
	if err != nil {
		return fail(fmt.Errorf("Codex app-server thread/start: %w", err))
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &thread); err != nil || thread.Thread.ID == "" {
		return fail(fmt.Errorf("Codex app-server thread/start returned no thread id"))
	}
	call.threadID = thread.Thread.ID
	onOutput("[Codex session started]\n")

	result, err = call.request(ctx, "turn/start", map[string]any{
		"threadId": call.threadID,
		"input":    []map[string]string{{"type": "text", "text": prompt}},
	})
	if err != nil {
		return fail(fmt.Errorf("Codex app-server turn/start: %w", err))
	}
	var started struct {
		Turn appServerTurn `json:"turn"`
	}
	if err := json.Unmarshal(result, &started); err != nil {
		return fail(fmt.Errorf("Codex app-server turn/start: %w", err))
	}
	if call.turnID == "" {
		call.turnID = started.Turn.ID
	}

	for !call.done {
		msg, err := call.next(ctx)
		if err != nil {
			return fail(err)
		}
		if err := call.dispatch(msg); err != nil {
			return fail(err)
		}
	}
	switch call.turn.Status {
	case "completed":
		return call.output(), nil
	case "failed":
		message := "unknown error"
		if call.turn.Error != nil && call.turn.Error.Message != "" {
			message = call.turn.Error.Message
		}
		return "", fmt.Errorf("Codex turn failed: %s", message)
	case "interrupted":
		return "", errors.New("Codex turn was interrupted")
	default:
		return "", fmt.Errorf("Codex turn ended with status %q", call.turn.Status)
	}
}

func readAppServer(r io.Reader, messages chan<- rpcMessage, readErr chan<- error, stop <-chan struct{}) {
	defer close(messages)
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 && trimmed[0] == '{' {
			var msg rpcMessage
			if jsonErr := json.Unmarshal(trimmed, &msg); jsonErr != nil {
				readErr <- fmt.Errorf("invalid Codex app-server message: %w", jsonErr)
				return
			}
			select {
			case messages <- msg:
			case <-stop:
				return
			}
		}
		if err != nil {
			readErr <- err
			return
		}
	}
}

func (c *appServerCall) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing to Codex app-server: %w", err)
	}
	return nil
}

func (c *appServerCall) next(ctx context.Context) (rpcMessage, error) {
	select {
	case msg, ok := <-c.messages:
		if ok {
			return msg, nil
		}
		err := io.ErrUnexpectedEOF
		select {
		case readErr := <-c.readErr:
			if readErr != io.EOF {
				err = readErr
			}
		default:
		}
		return rpcMessage{}, fmt.Errorf("Codex app-server stopped before the turn finished: %w", err)
	case <-ctx.Done():
		return rpcMessage{}, ctx.Err()
	}
}

// request sends a client request and handles notifications and server
// requests until its response arrives.
func (c *appServerCall) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.nextID++
	id := strconv.Itoa(c.nextID)
	if err := c.send(map[string]any{"id": c.nextID, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		msg, err := c.next(ctx)
		if err != nil {
			return nil, err
		}
		if msg.Method == "" && string(msg.ID) == id {
			if msg.Error != nil {
				return nil, fmt.Errorf("%s (code %d)", msg.Error.Message, msg.Error.Code)
			}
			return msg.Result, nil
		}
		if err := c.dispatch(msg); err != nil {
			return nil, err
		}
	}
}

func (c *appServerCall) dispatch(msg rpcMessage) error {
	switch {
	case msg.Method != "" && len(msg.ID) > 0:
		return c.answer(msg)
	case msg.Method != "":
		c.notify(msg)
	}
	return nil
}

// answer declines interactive server requests: SignalBox has no user to ask
// during a call, and an unanswered request would stall the turn.
func (c *appServerCall) answer(msg rpcMessage) error {
	var result any
	var label string
	switch msg.Method {
	case "item/commandExecution/requestApproval":
		result, label = map[string]string{"decision": "decline"}, "command approval"
	case "item/fileChange/requestApproval":
		result, label = map[string]string{"decision": "decline"}, "file change approval"
	case "mcpServer/elicitation/request":
		result, label = map[string]any{"action": "decline", "content": nil}, "MCP input request"
	}
	if result != nil {
		c.onOutput("[Codex " + label + " declined: SignalBox runs Codex non-interactively]\n")
		return c.send(map[string]any{"id": msg.ID, "result": result})
	}
	c.onOutput("[Codex request " + msg.Method + " is not supported by SignalBox]\n")
	return c.send(map[string]any{"id": msg.ID, "error": rpcError{Code: -32601, Message: "SignalBox runs Codex non-interactively and does not support " + msg.Method}})
}

// notify logs visible activity for this call's thread. Reasoning and other
// threads (for example sub-agents) are ignored.
func (c *appServerCall) notify(msg rpcMessage) {
	var event struct {
		ThreadID  string          `json:"threadId"`
		ItemID    string          `json:"itemId"`
		Delta     string          `json:"delta"`
		Item      appServerItem   `json:"item"`
		Turn      appServerTurn   `json:"turn"`
		WillRetry bool            `json:"willRetry"`
		Name      string          `json:"name"`
		Status    string          `json:"status"`
		Error     json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(msg.Params, &event); err != nil {
		return
	}
	if c.threadID != "" && event.ThreadID != "" && event.ThreadID != c.threadID {
		return
	}
	switch msg.Method {
	case "turn/started":
		if c.turnID == "" {
			c.turnID = event.Turn.ID
		}
		c.onOutput("[Codex working]\n")
	case "item/agentMessage/delta":
		c.streamed[event.ItemID] = true
		c.onOutput(event.Delta)
	case "item/started":
		if label := appServerActivity(event.Item); label != "" {
			c.onOutput("[" + label + " started]\n")
		}
	case "item/completed":
		item := event.Item
		if item.Type == "agentMessage" {
			if !c.streamed[item.ID] {
				c.onOutput(item.Text)
			}
			c.onOutput("\n")
			c.last = item.Text
			if item.Phase == "final_answer" {
				c.final = item.Text
			}
		} else if label := appServerActivity(item); label != "" {
			status := item.Status
			if status == "" {
				status = "completed"
			}
			c.onOutput("[" + label + " " + status + "]\n")
		}
	case "error":
		var turnError struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(event.Error, &turnError)
		if event.WillRetry {
			c.onOutput("[Codex error, retrying: " + turnError.Message + "]\n")
		} else {
			c.onOutput("[Codex error: " + turnError.Message + "]\n")
		}
	case "mcpServer/startupStatus/updated":
		if event.Status == "failed" {
			var detail string
			_ = json.Unmarshal(event.Error, &detail)
			c.onOutput("[MCP server " + event.Name + " failed to start: " + detail + "]\n")
		}
	case "turn/completed":
		if c.turnID == "" || event.Turn.ID == "" || event.Turn.ID == c.turnID {
			c.turn = event.Turn
			c.done = true
			if event.Turn.Status == "completed" {
				c.onOutput("[Codex turn completed]\n")
			}
		}
	}
}

// interrupt asks the server to stop an active turn so its tools are shut down
// cleanly; the caller kills the process if this does not finish in time.
func (c *appServerCall) interrupt() {
	if c.threadID == "" || c.turnID == "" || c.done {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), appServerGrace)
	defer cancel()
	c.nextID++
	if err := c.send(map[string]any{"id": c.nextID, "method": "turn/interrupt", "params": map[string]string{"threadId": c.threadID, "turnId": c.turnID}}); err != nil {
		return
	}
	for !c.done {
		msg, err := c.next(ctx)
		if err != nil || c.dispatch(msg) != nil {
			return
		}
	}
}

// output prefers the turn's final answer, falling back to the last agent
// message, as `codex exec` does.
func (c *appServerCall) output() string {
	if c.final != "" {
		return strings.TrimSpace(c.final)
	}
	if c.last != "" {
		return strings.TrimSpace(c.last)
	}
	for i := len(c.turn.Items) - 1; i >= 0; i-- {
		if c.turn.Items[i].Type == "agentMessage" {
			return strings.TrimSpace(c.turn.Items[i].Text)
		}
	}
	return ""
}

func appServerActivity(item appServerItem) string {
	switch item.Type {
	case "commandExecution":
		return "command execution"
	case "fileChange":
		return "file change"
	case "webSearch":
		return "web search"
	case "mcpToolCall":
		return "mcp tool call: " + item.Server + "." + item.Tool
	case "dynamicToolCall":
		return "tool call: " + item.Tool
	case "collabAgentToolCall":
		return "agent tool call: " + item.Tool
	case "imageGeneration":
		return "image generation"
	}
	return ""
}

// tailBuffer keeps the most recent output of a stream for error messages.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
