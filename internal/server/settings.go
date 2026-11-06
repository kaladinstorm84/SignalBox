package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

type settingsDocument struct {
	Content string `json:"content"`
	Version string `json:"version"`
}

func settingsVersion(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, settingsDocument{Content: string(content), Version: settingsVersion(content)})
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
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
	var proposed settingsDocument
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&proposed); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid settings request: %w", err))
		return
	}
	cfg, err := config.Parse([]byte(proposed.Content), s.projectRoot)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := runtime.NewRegistry(cfg.Runtimes); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	pipelines, errs := pipeline.LoadDir(cfg.PipelinesPath(), pipeline.Options{Root: cfg.Root, AllowExternal: cfg.AllowExternalImports, SchemasDir: cfg.SchemasPath()})
	if len(errs) > 0 {
		writeErr(w, http.StatusBadRequest, errs[0])
		return
	}
	if len(pipelines) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no pipelines found in %s", cfg.PipelinesPath()))
		return
	}
	if err := runtime.ValidateProject(cfg, pipelines); err != nil {
		writeErr(w, http.StatusBadRequest, err)
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
	if err := replaceSettings(path, []byte(proposed.Content)); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("saving settings: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, settingsDocument{Content: proposed.Content, Version: settingsVersion([]byte(proposed.Content))})
}

func replaceSettings(path string, content []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
