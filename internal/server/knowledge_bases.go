package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/kaladinstorm84/SignalBox/internal/knowledgebase"
)

func (s *Server) knowledgeBaseStore() (*knowledgebase.Store, error) {
	if s.projectRoot == "" {
		return nil, fmt.Errorf("project knowledge bases are not configured")
	}
	return knowledgebase.New(s.projectRoot), nil
}

func (s *Server) handleListKnowledgeBases(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.knowledgeBaseStore()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	bases, err := store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("listing knowledge bases: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, bases)
}

func (s *Server) handleCreateKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin knowledge base updates are not allowed"))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid knowledge base request: %w", err))
		return
	}
	store, err := s.knowledgeBaseStore()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.settingsMu.Lock()
	err = store.Create(req.Name)
	s.settingsMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": req.Name})
}

func (s *Server) handleDeleteKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin knowledge base updates are not allowed"))
		return
	}
	store, err := s.knowledgeBaseStore()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.settingsMu.Lock()
	err = store.Delete(r.PathValue("name"))
	s.settingsMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSaveKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin knowledge base updates are not allowed"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, knowledgebase.MaxDocumentBytes*6+4096)
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid knowledge document request: %w", err))
		return
	}
	s.saveKnowledgeDocument(w, r, []byte(req.Content))
}

func (s *Server) handleUploadKnowledgeDocuments(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin knowledge base updates are not allowed"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 10*knowledgebase.MaxDocumentBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid knowledge document upload: %w", err))
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 || len(files) > 10 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("upload between 1 and 10 text documents"))
		return
	}
	store, err := s.knowledgeBaseStore()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	name := r.PathValue("name")
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	type uploadedDocument struct {
		name string
		data []byte
	}
	documents := make([]uploadedDocument, 0, len(files))
	seen := map[string]bool{}
	for _, file := range files {
		if !knowledgebase.ValidDocumentName(file.Filename) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("document %q must be a plain-text file (txt, md, csv, tsv, json, yaml, yml, xml or log)", file.Filename))
			return
		}
		if seen[file.Filename] {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("document %q was included more than once", file.Filename))
			return
		}
		seen[file.Filename] = true
		if file.Size > knowledgebase.MaxDocumentBytes {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("document %q exceeds the %d-byte limit", file.Filename, knowledgebase.MaxDocumentBytes))
			return
		}
		src, err := file.Open()
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("opening document %q: %w", file.Filename, err))
			return
		}
		data, readErr := io.ReadAll(io.LimitReader(src, knowledgebase.MaxDocumentBytes+1))
		closeErr := src.Close()
		if readErr != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("reading document %q: %w", file.Filename, readErr))
			return
		}
		if closeErr != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("closing document %q: %w", file.Filename, closeErr))
			return
		}
		documents = append(documents, uploadedDocument{name: file.Filename, data: data})
	}
	for _, doc := range documents {
		if err := store.SaveDocument(name, doc.name, doc.data); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uploaded": len(files)})
}

func (s *Server) saveKnowledgeDocument(w http.ResponseWriter, r *http.Request, content []byte) {
	store, err := s.knowledgeBaseStore()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.settingsMu.Lock()
	err = store.SaveDocument(r.PathValue("name"), r.PathValue("filename"), content)
	s.settingsMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": r.PathValue("filename")})
}

func (s *Server) handleDeleteKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("cross-origin knowledge base updates are not allowed"))
		return
	}
	store, err := s.knowledgeBaseStore()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.settingsMu.Lock()
	err = store.DeleteDocument(r.PathValue("name"), r.PathValue("filename"))
	s.settingsMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == r.Host
}
