package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"unicode/utf8"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/plugin"
	"github.com/kaladinstorm84/SignalBox/internal/registry"
)

func (s *Server) openRegistry() (*config.Config, *registry.Registry, error) {
	cfg, err := s.pipelineFileConfig()
	if err != nil {
		return nil, nil, err
	}
	reg, err := registry.Open(cfg.RegistryPath())
	if err != nil {
		return cfg, nil, err
	}
	return cfg, reg, nil
}

// handleRegistry searches the configured registry and reports installed and
// publishable pipelines and knowledge bases. An unconfigured registry is not an error.
func (s *Server) handleRegistry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg, err := s.pipelineFileConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	lock, err := registry.ReadLock(cfg.Root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	publishable, publishErr := registry.Publishable(cfg)
	knowledge, err := registry.PublishableKnowledge(cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("listing publishable knowledge bases: %w", err))
		return
	}
	plugins, err := plugin.List(cfg.Root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("listing publishable plugins: %w", err))
		return
	}
	resp := map[string]any{
		"configured":            cfg.Registry.Path != "",
		"path":                  cfg.RegistryPath(),
		"installed":             lock.Packages,
		"publishable":           publishable,
		"publishable_knowledge": knowledge,
		"publishable_plugins":   plugins,
		"packages":              []registry.Summary{},
	}
	if publishErr != nil {
		resp["publish_error"] = publishErr.Error()
	}
	if cfg.Registry.Path != "" {
		reg, err := registry.Open(cfg.RegistryPath())
		if err != nil {
			resp["error"] = err.Error()
		} else if packages, err := reg.Search(r.URL.Query().Get("q")); err != nil {
			resp["error"] = err.Error()
		} else {
			resp["packages"] = packages
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRegistryPackage returns a manifest, its sibling versions, and the
// verified contents of the package's text files for inspection.
func (s *Server) handleRegistryPackage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, reg, err := s.openRegistry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ref := r.PathValue("name")
	if v := r.PathValue("version"); v != "" && v != "latest" {
		ref += "@" + v
	}
	name, version, err := reg.Resolve(ref)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	m, err := reg.Manifest(name, version)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	versions, _ := reg.Versions(name)
	files := map[string]string{}
	verifyErr := ""
	for _, f := range m.Files {
		data, err := reg.ReadFile(m, f.Path)
		if err != nil {
			verifyErr = err.Error()
			continue
		}
		if !utf8.Valid(data) || bytes.ContainsRune(data, '\x00') {
			files[f.Path] = fmt.Sprintf("[Binary file: %d bytes; included in package and integrity verification]", len(data))
		} else if len(data) > 256<<10 {
			files[f.Path] = fmt.Sprintf("[File too large to preview: %d bytes; included in package and integrity verification]", len(data))
		} else {
			files[f.Path] = string(data)
		}
	}
	if verifyErr == "" {
		if _, err := reg.Verify(m); err != nil {
			verifyErr = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"manifest":     m,
		"versions":     versions,
		"files":        files,
		"verify_error": verifyErr,
	})
}

func (s *Server) handleRegistryInstall(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin registry updates are not allowed"))
		return
	}
	var req struct {
		Ref    string `json:"ref"`
		Digest string `json:"digest"`
		Force  bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Ref == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("ref (name or name@version) is required"))
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	cfg, reg, err := s.openRegistry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	result, err := reg.Install(cfg, req.Ref, registry.InstallOptions{Digest: req.Digest, Force: req.Force})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if result.Manifest.PackageKind() == registry.KindPipeline || result.Manifest.PackageKind() == registry.KindPlugin {
		if err := s.refreshPipelines(); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("installed, but reloading pipelines failed: %w", err))
			return
		}
	}
	log.Printf("installed %s@%s from registry", result.Manifest.Name, result.Manifest.Version)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRegistryPublish(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin registry updates are not allowed"))
		return
	}
	var req struct {
		Pipeline      string `json:"pipeline"`
		KnowledgeBase string `json:"knowledge_base"`
		Plugin        string `json:"plugin"`
		Description   string `json:"description"`
		Version       string `json:"version"`
		By            string `json:"by"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid publish request: %w", err))
		return
	}
	selections := 0
	for _, name := range []string{req.Pipeline, req.KnowledgeBase, req.Plugin} {
		if name != "" {
			selections++
		}
	}
	if selections != 1 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("exactly one of pipeline, knowledge_base or plugin is required"))
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	cfg, reg, err := s.openRegistry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	opts := registry.PublishOptions{Version: req.Version, By: req.By}
	var m *registry.Manifest
	if req.Plugin != "" {
		m, err = reg.PublishPlugin(cfg, req.Plugin, opts)
	} else if req.KnowledgeBase != "" {
		m, err = reg.PublishKnowledge(cfg, req.KnowledgeBase, req.Description, opts)
	} else {
		m, err = reg.Publish(cfg, req.Pipeline, opts)
	}
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	log.Printf("published %s@%s to registry", m.Name, m.Version)
	writeJSON(w, http.StatusCreated, m)
}
