package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

type pipelineFile struct {
	Filename string `json:"filename"`
	Name     string `json:"name,omitempty"`
}

func validPipelineFilename(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return (ext == ".yaml" || ext == ".yml") && len(filename) > len(ext) &&
		strings.TrimSpace(filename) == filename && !strings.ContainsAny(filename, `/\:<>"|?*`) &&
		filepath.Base(filename) == filename
}

func (s *Server) pipelineFileConfig() (*config.Config, error) {
	if s.projectRoot == "" {
		return nil, fmt.Errorf("project settings are not configured")
	}
	return config.Load(s.projectRoot)
}

func (s *Server) handleListPipelineFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg, err := s.pipelineFileConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	entries, err := os.ReadDir(cfg.PipelinesPath())
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	files := []pipelineFile{}
	for _, entry := range entries {
		if entry.Type().IsRegular() && validPipelineFilename(entry.Name()) {
			file := pipelineFile{Filename: entry.Name()}
			if p, err := pipeline.Load(filepath.Join(cfg.PipelinesPath(), entry.Name())); err == nil {
				file.Name = p.Name
			}
			files = append(files, file)
		}
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleGetPipelineFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	filename := r.PathValue("filename")
	if !validPipelineFilename(filename) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("filename must be a YAML file without directory components"))
		return
	}
	cfg, err := s.pipelineFileConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	path := filepath.Join(cfg.PipelinesPath(), filename)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("pipeline file %q does not exist", filename))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("pipeline file must be a regular file, not a symbolic link or directory"))
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDocument{Content: string(content), Version: settingsVersion(content)})
}

func (s *Server) handleSavePipelineFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host {
			writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin pipeline updates are not allowed"))
			return
		}
	}
	filename := r.PathValue("filename")
	if !validPipelineFilename(filename) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("filename must be a YAML file without directory components"))
		return
	}
	var proposed settingsDocument
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&proposed); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid pipeline request: %w", err))
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	cfg, err := s.pipelineFileConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	p, err := pipeline.Parse([]byte(proposed.Content))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	for _, node := range p.Nodes {
		if node.Runtime != "" {
			if _, ok := cfg.Runtimes[node.Runtime]; !ok {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("agent %q references undefined runtime %q", node.ID, node.Runtime))
				return
			}
		}
		if node.Type == pipeline.TypeCode {
			if _, err := runtime.NewCode(&node, cfg.Root); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
	}
	dir := cfg.PipelinesPath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !validPipelineFilename(entry.Name()) || strings.EqualFold(entry.Name(), filename) {
			continue
		}
		other, err := pipeline.Load(filepath.Join(dir, entry.Name()))
		if err == nil && other.Name == p.Name {
			writeErr(w, http.StatusConflict, fmt.Errorf("pipeline name %q is already used by %s", p.Name, entry.Name()))
			return
		}
	}
	path := filepath.Join(dir, filename)
	info, err := os.Lstat(path)
	creating := os.IsNotExist(err)
	if err != nil && !creating {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !creating && !info.Mode().IsRegular() {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("pipeline file must be a regular file, not a symbolic link or directory"))
		return
	}
	if creating {
		if proposed.Version != "" {
			writeErr(w, http.StatusConflict, fmt.Errorf("pipeline file was removed; reload before saving"))
			return
		}
		err = createPipelineFile(path, []byte(proposed.Content))
	} else {
		var current []byte
		current, err = os.ReadFile(path)
		if err == nil && (proposed.Version == "" || proposed.Version != settingsVersion(current)) {
			writeErr(w, http.StatusConflict, fmt.Errorf("pipeline file already exists or changed on disk; reload before saving"))
			return
		}
		if err == nil {
			if previous, parseErr := pipeline.Parse(current); parseErr == nil && previous.Name != p.Name {
				for _, watcher := range cfg.Watchers {
					if watcher.Pipeline == previous.Name {
						writeErr(w, http.StatusBadRequest, fmt.Errorf("watcher %q still references pipeline %q; update its target before renaming this pipeline", watcher.Name, previous.Name))
						return
					}
				}
			}
		}
		if err == nil {
			err = replaceSettings(path, []byte(proposed.Content))
		}
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("saving pipeline: %w", err))
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	writeJSON(w, status, settingsDocument{Content: proposed.Content, Version: settingsVersion([]byte(proposed.Content))})
}

func createPipelineFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	return nil
}
