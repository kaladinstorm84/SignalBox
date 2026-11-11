package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"gopkg.in/yaml.v3"
)

type watcherEntry struct {
	OriginalName   string   `json:"original_name"`
	Name           string   `json:"name"`
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	Interval       string   `json:"interval"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Pipeline       string   `json:"pipeline"`
}

type watcherDocument struct {
	Version  string         `json:"version"`
	Watchers []watcherEntry `json:"watchers"`
}

func watcherEntries(cfg *config.Config) []watcherEntry {
	entries := make([]watcherEntry, 0, len(cfg.Watchers))
	for _, w := range cfg.Watchers {
		entries = append(entries, watcherEntry{w.Name, w.Name, w.Command, w.Args, w.Interval, w.TimeoutSeconds, w.Pipeline})
	}
	return entries
}

func (s *Server) handleGetWatchers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.projectRoot == "" {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("project settings are not configured"))
		return
	}
	content, err := os.ReadFile(filepath.Join(s.projectRoot, config.FileName))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	cfg, err := config.Parse(content, s.projectRoot)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, watcherDocument{settingsVersion(content), watcherEntries(cfg)})
}

func (s *Server) handleSaveWatchers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host {
			writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin settings updates are not allowed"))
			return
		}
	}
	if s.projectRoot == "" {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("project settings are not configured"))
		return
	}
	var proposed watcherDocument
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&proposed); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid watcher request: %w", err))
		return
	}
	if proposed.Watchers == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("watchers must be an array"))
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	path := filepath.Join(s.projectRoot, config.FileName)
	current, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if proposed.Version != settingsVersion(current) {
		writeErr(w, http.StatusConflict, fmt.Errorf("settings changed on disk; reload before saving"))
		return
	}
	content, err := updateWatchersYAML(current, proposed.Watchers)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.validateSettings(content); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := replaceSettings(path, content); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("saving settings: %w", err))
		return
	}
	cfg, err := config.Parse(content, s.projectRoot)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, watcherDocument{settingsVersion(content), watcherEntries(cfg)})
}

// Rewrite only the watchers section, leaving the rest of the settings file intact.
func updateWatchersYAML(content []byte, entries []watcherEntry) ([]byte, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid settings YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("invalid settings YAML: %w", err)
		}
		return nil, fmt.Errorf("edit multi-document settings in the Settings screen")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || doc.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("edit flow-style or non-mapping settings in the Settings screen")
	}
	root := doc.Content[0]
	var key, value *yaml.Node
	nextLine := 0
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "watchers" {
			key, value = root.Content[i], root.Content[i+1]
			if i+2 < len(root.Content) {
				nextLine = root.Content[i+2].Line
			}
			break
		}
	}
	if key != nil && (key.Column != 1 || (value.Kind != yaml.SequenceNode && value.Tag != "!!null")) {
		return nil, fmt.Errorf("edit this watchers section in the Settings screen")
	}
	old := map[string]*yaml.Node{}
	if value != nil && value.Kind == yaml.SequenceNode {
		for _, node := range value.Content {
			if node.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("edit this watchers section in the Settings screen")
			}
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == "name" {
					old[node.Content[i+1].Value] = node
				}
			}
		}
	}
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	seen := map[string]bool{}
	for _, entry := range entries {
		var node *yaml.Node
		if entry.OriginalName != "" {
			if seen[entry.OriginalName] || old[entry.OriginalName] == nil {
				return nil, fmt.Errorf("invalid original watcher name %q; reload before saving", entry.OriginalName)
			}
			seen[entry.OriginalName] = true
			node = old[entry.OriginalName]
		} else {
			node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		fields := []struct {
			name  string
			value interface{}
		}{
			{"name", entry.Name}, {"command", entry.Command}, {"args", entry.Args},
			{"interval", entry.Interval}, {"timeout_seconds", entry.TimeoutSeconds}, {"pipeline", entry.Pipeline},
		}
		for _, field := range fields {
			var encoded yaml.Node
			if field.name == "args" && entry.Args == nil {
				field.value = []string{}
			}
			if err := encoded.Encode(field.value); err != nil {
				return nil, err
			}
			found := false
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == field.name {
					existing := node.Content[i+1]
					previous := reflect.New(reflect.TypeOf(field.value))
					if err := existing.Decode(previous.Interface()); err != nil {
						return nil, fmt.Errorf("invalid watcher %q field %q: %w", entry.OriginalName, field.name, err)
					}
					if reflect.DeepEqual(previous.Elem().Interface(), field.value) {
						found = true
						break
					}
					encoded.HeadComment, encoded.LineComment, encoded.FootComment = existing.HeadComment, existing.LineComment, existing.FootComment
					node.Content[i+1] = &encoded
					found = true
					break
				}
			}
			if !found {
				node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: field.name}, &encoded)
			}
		}
		sequence.Content = append(sequence.Content, node)
	}
	if key == nil {
		key = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "watchers"}
	} else {
		copyKey := *key
		copyKey.HeadComment = ""
		key = &copyKey
	}
	if value != nil {
		sequence.HeadComment, sequence.LineComment, sequence.FootComment = value.HeadComment, value.LineComment, value.FootComment
	}
	section := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{key, sequence}}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(section); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	newline := "\n"
	if bytes.Contains(content, []byte("\r\n")) {
		newline = "\r\n"
	}
	lines := strings.SplitAfter(string(content), "\n")
	start, end := len(lines), len(lines)
	if value != nil {
		start = key.Line - 1
		if nextLine > 0 {
			end = nextLine - 1
		} else {
			for i := start + 1; i < len(lines); i++ {
				if strings.TrimSpace(lines[i]) == "..." {
					end = i
					break
				}
			}
		}
		for end > start+1 && (strings.TrimSpace(lines[end-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[end-1]), "#")) {
			end--
		}
	} else {
		for i, line := range lines {
			if strings.TrimSpace(line) == "..." {
				start, end = i, i
				break
			}
		}
	}
	prefix := strings.Join(lines[:start], "")
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += newline
	}
	replacement := strings.ReplaceAll(buf.String(), "\n", newline)
	return []byte(prefix + replacement + strings.Join(lines[end:], "")), nil
}
