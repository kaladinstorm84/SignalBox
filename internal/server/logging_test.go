package server

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPErrorResponsesAreLogged(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	recorder := httptest.NewRecorder()
	writeErr(recorder, http.StatusBadRequest, errors.New("invalid execution input"))
	if !strings.Contains(output.String(), "HTTP 400 error:") || !strings.Contains(output.String(), "invalid execution input") {
		t.Fatalf("HTTP error not logged: %s", output.String())
	}
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"error":"invalid execution input"`) {
		t.Fatalf("error response changed: %d %s", recorder.Code, recorder.Body.String())
	}
	output.Reset()
	writeJSON(httptest.NewRecorder(), http.StatusConflict, map[string]any{
		"error": "pipeline definition changed", "definition_changed": true,
	})
	if !strings.Contains(output.String(), "HTTP 409 error:") {
		t.Fatalf("structured conflict not logged: %s", output.String())
	}
	output.Reset()
	writeJSON(httptest.NewRecorder(), http.StatusOK, map[string]string{"status": "ok"})
	if output.Len() != 0 {
		t.Fatalf("successful response logged as error: %s", output.String())
	}
}
