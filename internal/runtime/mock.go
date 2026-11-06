package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Mock is a deterministic runtime for tests and the out-of-the-box demo
// pipeline. It echoes a summary of what it was asked to do.
type Mock struct {
	RuntimeName string
	// Handler, when set, overrides the default echo behaviour (tests).
	Handler func(ctx context.Context, req Request) (string, error)
	// Delay simulates processing time.
	Delay time.Duration
}

func (m *Mock) Name() string {
	if m.RuntimeName == "" {
		return "mock"
	}
	return m.RuntimeName
}

func (m *Mock) Invoke(ctx context.Context, req Request) (string, error) {
	if m.Delay > 0 {
		select {
		case <-time.After(m.Delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if m.Handler != nil {
		return m.Handler(ctx, req)
	}
	if req.Schema != nil {
		return req.Schema.Sample(), nil
	}
	role := firstLine(req.SystemPrompt)
	if role == "" {
		role = "agent"
	}
	out := fmt.Sprintf("[mock:%s] %s\n\nProcessed input:\n%s", m.Name(), role, req.Input)
	if len(req.Files) > 0 {
		out += "\n\nFiles received at " + FilesDirEnv + ":"
		for _, path := range req.Files {
			out += "\n- " + filepath.Base(path)
		}
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
