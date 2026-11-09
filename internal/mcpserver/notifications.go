package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) refreshPipelines(ctx context.Context) error {
	data, _, err := s.request(ctx, http.MethodGet, "/pipelines", nil)
	if err != nil {
		return err
	}
	var pipelines []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &pipelines); err != nil {
		return fmt.Errorf("decoding pipeline catalog: %w", err)
	}
	current := make(map[string]string, len(pipelines))
	for _, pipeline := range pipelines {
		if _, err := segment(pipeline.Name); err != nil {
			return fmt.Errorf("invalid pipeline name from API: %w", err)
		}
		current[pipeline.Name] = pipeline.Description
		if description, ok := s.pipelines[pipeline.Name]; !ok || description != pipeline.Description {
			s.server.AddResource(&mcp.Resource{
				URI:  "signalbox://pipelines/" + url.PathEscape(pipeline.Name),
				Name: pipeline.Name, Description: pipeline.Description, MIMEType: "application/json",
			}, s.readResource)
		}
	}
	for name := range s.pipelines {
		if _, ok := current[name]; !ok {
			s.server.RemoveResources("signalbox://pipelines/" + url.PathEscape(name))
		}
	}
	s.pipelines = current
	return nil
}

func fingerprint(result *mcp.ReadResourceResult, err error) string {
	if err != nil {
		return "error:" + err.Error()
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "error:" + err.Error()
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (s *Server) subscribe(ctx context.Context, req *mcp.SubscribeRequest) error {
	result, err := s.readURI(ctx, req.Params.URI)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subscriptions[req.Session] == nil {
		s.subscriptions[req.Session] = map[string]string{}
	}
	s.subscriptions[req.Session][req.Params.URI] = fingerprint(result, nil)
	return nil
}

func (s *Server) unsubscribe(_ context.Context, req *mcp.UnsubscribeRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscriptions[req.Session], req.Params.URI)
	return nil
}

func (s *Server) poll(ctx context.Context) error {
	var errs []error
	if err := s.refreshPipelines(ctx); err != nil {
		errs = append(errs, err)
	}
	active := map[*mcp.ServerSession]bool{}
	for session := range s.server.Sessions() {
		active[session] = true
	}
	s.mu.Lock()
	uris := map[string]bool{}
	for session, resources := range s.subscriptions {
		if !active[session] {
			delete(s.subscriptions, session)
			continue
		}
		for uri := range resources {
			uris[uri] = true
		}
	}
	s.mu.Unlock()
	for uri := range uris {
		result, err := s.readURI(ctx, uri)
		digest := fingerprint(result, err)
		changed := false
		s.mu.Lock()
		for _, resources := range s.subscriptions {
			if previous, ok := resources[uri]; ok && previous != digest {
				resources[uri] = digest
				changed = true
			}
		}
		s.mu.Unlock()
		if changed {
			if err != nil {
				errs = append(errs, fmt.Errorf("reading subscribed resource %s: %w", uri, err))
			}
			if err := s.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
