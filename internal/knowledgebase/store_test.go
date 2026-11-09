package knowledgebase

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreContentAndValidation(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if err := store.Create("handbook"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDocument("handbook", "z-last.md", []byte("last")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDocument("handbook", "a-first.txt", []byte("first")); err != nil {
		t.Fatal(err)
	}
	content, err := store.Content([]string{"handbook"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(content, "a-first.txt") > strings.Index(content, "z-last.md") {
		t.Fatalf("documents are not sorted: %q", content)
	}
	if !strings.Contains(content, "first") || !strings.Contains(content, "last") {
		t.Fatalf("content = %q", content)
	}
	bases, err := store.List()
	if err != nil || len(bases) != 1 || len(bases[0].Documents) != 2 {
		t.Fatalf("bases = %+v, error = %v", bases, err)
	}
	for _, test := range []struct {
		base string
		name string
		data []byte
	}{
		{base: "../outside", name: "ok.txt", data: []byte("x")},
		{base: "handbook", name: "../outside.txt", data: []byte("x")},
		{base: "handbook", name: "image.png", data: []byte("x")},
		{base: "handbook", name: "invalid.txt", data: []byte{0xff}},
		{base: "handbook", name: "large.txt", data: make([]byte, MaxDocumentBytes+1)},
	} {
		if err := store.SaveDocument(test.base, test.name, test.data); err == nil {
			t.Errorf("accepted invalid document %q in %q", test.name, test.base)
		}
	}
	if _, err := store.Content([]string{"missing"}); err == nil {
		t.Fatal("missing knowledge base did not return an error")
	}
	if _, err := New(filepath.Join(root, "not-created")).Content([]string{"missing"}); err == nil || !strings.Contains(err.Error(), `knowledge base "missing" does not exist`) {
		t.Fatalf("missing knowledge base error = %v", err)
	}
	if err := store.DeleteDocument("handbook", "a-first.txt"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("handbook"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "knowledge-bases", "handbook")); !os.IsNotExist(err) {
		t.Fatalf("knowledge base was not deleted: %v", err)
	}
}
