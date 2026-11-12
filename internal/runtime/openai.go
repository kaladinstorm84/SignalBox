package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

// openAI calls any OpenAI-compatible chat completions endpoint.
type openAI struct {
	options config.RuntimeConfig
	name    string
	model   string
	baseURL string
	keyEnv  string
	timeout time.Duration
	client  *http.Client
}

func newOpenAI(name string, rc config.RuntimeConfig) (Runtime, error) {
	if rc.Model == "" {
		return nil, fmt.Errorf("openai runtime requires a model")
	}
	baseURL := rc.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	keyEnv := rc.APIKeyEnv
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	timeout := time.Duration(rc.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	return &openAI{
		options: rc,
		name:    name,
		model:   rc.Model,
		baseURL: strings.TrimRight(baseURL, "/"),
		keyEnv:  keyEnv,
		timeout: timeout,
		client:  &http.Client{},
	}, nil
}

func (o *openAI) Name() string { return o.name }

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	ServiceTier     string          `json:"service_tier,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
}

type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *jsonSchemaSpec `json:"json_schema,omitempty"`
}

type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *openAI) Invoke(ctx context.Context, req Request) (string, error) {
	return o.invoke(ctx, req, nil, false)
}

func (o *openAI) InvokeStream(ctx context.Context, req Request, onOutput func(string)) (string, error) {
	return o.invoke(ctx, req, onOutput, true)
}

func (o *openAI) invoke(ctx context.Context, req Request, onOutput func(string), stream bool) (string, error) {
	options, err := requestOptions(o.options, req)
	if err != nil {
		return "", err
	}
	key := os.Getenv(o.keyEnv)
	if key == "" {
		return "", fmt.Errorf("environment variable %s is not set", o.keyEnv)
	}
	messages := []chatMessage{}
	if req.SystemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.SystemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: req.Input})

	model := o.model
	if req.Model != "" {
		model = req.Model
	}
	tier := ""
	if options.Speed == "fast" {
		tier = "priority"
	} else if options.Speed == "standard" {
		tier = "default"
	}
	timeout, err := invocationTimeout(o.timeout, req.TimeoutSeconds)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, ResponseFormat: responseFormatFor(req), ReasoningEffort: options.ReasoningEffort, ServiceTier: tier, Stream: stream})
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request to %s failed: %w", o.baseURL, err)
	}
	defer resp.Body.Close()
	if stream {
		if resp.StatusCode != http.StatusOK {
			respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
			if readErr != nil {
				return "", readErr
			}
			return "", fmt.Errorf("API returned HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
		}
		if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
			if readErr != nil {
				return "", readErr
			}
			return parseChatResponse(respBody, onOutput, resp.StatusCode)
		}
		return readChatStream(resp.Body, onOutput)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}
	return parseChatResponse(respBody, nil, resp.StatusCode)
}

func parseChatResponse(body []byte, onOutput func(string), statusCode int) (string, error) {
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("unexpected response (HTTP %d): %s", statusCode, truncate(string(body), 500))
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("API error (HTTP %d): %s", statusCode, parsed.Error.Message)
	}
	if statusCode != http.StatusOK {
		return "", fmt.Errorf("API returned HTTP %d: %s", statusCode, truncate(string(body), 500))
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("API returned no choices")
	}
	output := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if output != "" && onOutput != nil {
		onOutput(output)
	}
	return output, nil
}

type chatStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func readChatStream(body io.Reader, onOutput func(string)) (string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 10<<20)
	var output strings.Builder
	var data strings.Builder
	flush := func() (bool, error) {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return false, nil
		}
		if payload == "[DONE]" {
			return true, nil
		}
		var event chatStreamResponse
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return false, fmt.Errorf("invalid streamed API response: %w", err)
		}
		if event.Error != nil {
			return false, fmt.Errorf("API stream error: %s", event.Error.Message)
		}
		for _, choice := range event.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			output.WriteString(choice.Delta.Content)
			if onOutput != nil {
				onOutput(choice.Delta.Content)
			}
		}
		return false, nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := flush()
			if err != nil {
				return "", err
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading streamed API response: %w", err)
	}
	if data.Len() > 0 {
		if _, err := flush(); err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(output.String()), nil
}

// responseFormatFor requests native structured output. Strict mode is off
// because it only accepts a subset of JSON Schema; SignalBox validates the
// response itself.
func responseFormatFor(req Request) *responseFormat {
	if req.Schema == nil {
		return nil
	}
	return &responseFormat{Type: "json_schema", JSONSchema: &jsonSchemaSpec{
		Name:   openAISchemaName(req.Schema.Name),
		Schema: json.RawMessage(req.Schema.JSON),
	}}
}

// openAISchemaName keeps to the API's name rules: a-z, A-Z, 0-9, _ and -,
// at most 64 characters.
func openAISchemaName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "response"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
