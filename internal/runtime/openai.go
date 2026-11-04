package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gitea/signalbox/internal/config"
)

// openAI calls any OpenAI-compatible chat completions endpoint.
type openAI struct {
	name    string
	model   string
	baseURL string
	keyEnv  string
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
		name:    name,
		model:   rc.Model,
		baseURL: strings.TrimRight(baseURL, "/"),
		keyEnv:  keyEnv,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func (o *openAI) Name() string { return o.name }

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
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
	key := os.Getenv(o.keyEnv)
	if key == "" {
		return "", fmt.Errorf("environment variable %s is not set", o.keyEnv)
	}
	messages := []chatMessage{}
	if req.SystemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.SystemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: req.Input})

	body, err := json.Marshal(chatRequest{Model: o.model, Messages: messages})
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
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return "", err
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unexpected response (HTTP %d): %s", resp.StatusCode, truncate(string(respBody), 500))
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("API returned no choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
