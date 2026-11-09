package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/knowledgebase"
)

func TestKnowledgeBaseAPI(t *testing.T) {
	root := t.TempDir()
	handler := New(nil, "test", nil, nil, nil, root).Handler()
	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return response
	}
	if response := request(http.MethodPost, "/api/knowledge-bases", []byte(`{"name":"handbook"}`)); response.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPut, "/api/knowledge-bases/handbook/documents/overview.md", []byte(`{"content":"Useful project context"}`)); response.Code != http.StatusCreated {
		t.Fatalf("save document = %d: %s", response.Code, response.Body.String())
	}
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "policies.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("Policy text")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	requestUpload := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/handbook/documents", &upload)
	requestUpload.Header.Set("Content-Type", writer.FormDataContentType())
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, requestUpload)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	list := request(http.MethodGet, "/api/knowledge-bases", nil)
	if list.Code != http.StatusOK || list.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list = %d: %s", list.Code, list.Body.String())
	}
	var bases []knowledgebase.Base
	if err := json.Unmarshal(list.Body.Bytes(), &bases); err != nil {
		t.Fatal(err)
	}
	if len(bases) != 1 || bases[0].Name != "handbook" || len(bases[0].Documents) != 2 {
		t.Fatalf("bases = %+v", bases)
	}
	if response := request(http.MethodPut, "/api/knowledge-bases/handbook/documents/unsafe.pdf", []byte(`{"content":"no"}`)); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported document = %d: %s", response.Code, response.Body.String())
	}
	foreign := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/handbook", nil)
	foreign.Header.Set("Origin", "https://unrelated.example")
	foreignResponse := httptest.NewRecorder()
	handler.ServeHTTP(foreignResponse, foreign)
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatalf("foreign delete = %d", foreignResponse.Code)
	}
	if response := request(http.MethodDelete, "/api/knowledge-bases/handbook/documents/overview.md", nil); response.Code != http.StatusNoContent {
		t.Fatalf("delete document = %d: %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodDelete, "/api/knowledge-bases/handbook", nil); response.Code != http.StatusNoContent {
		t.Fatalf("delete base = %d: %s", response.Code, response.Body.String())
	}
}
