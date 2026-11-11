package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleFiles() map[string][]byte {
	return map[string][]byte{
		Descriptor:        []byte("language: python\nentry: main.py\n"),
		"main.py":         []byte("from helper import value\nprint(value)\n"),
		"helper.py":       []byte("value = 42\n"),
		"assets/data.bin": {0, 1, 255},
	}
}

func TestDefinitionDigestIncludesEntireFolder(t *testing.T) {
	files := sampleFiles()
	a, err := FromFiles("example", files)
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromFiles("example", files)
	if err != nil || b.Digest != a.Digest {
		t.Fatalf("unstable digest: %v, %v", b, err)
	}
	files["assets/data.bin"][0] = 9
	c, err := FromFiles("example", files)
	if err != nil || c.Digest == a.Digest {
		t.Fatalf("resource change not fingerprinted: %v, %v", c, err)
	}
	files[".settings.json"] = []byte("{}")
	if _, err := FromFiles("example", files); err != nil {
		t.Fatalf("portable dotfile rejected: %v", err)
	}
}

func TestInvalidDefinitions(t *testing.T) {
	for _, descriptor := range []string{
		"language: ruby\nentry: main.py\n",
		"language: python\nentry: ../main.py\n",
		"language: python\nentry: absent.py\n",
		"language: python\nentry: main.py\nparams: {}\n",
		"language: python\nentry: main.py\n---\n",
		"language: python\nentry: main.py\ntimeout_seconds: -1\n",
		"language: python\nentry: main.py\ncodes: {success: 0, fail: 0, retry: 2}\n",
	} {
		t.Run(descriptor, func(t *testing.T) {
			files := sampleFiles()
			files[Descriptor] = []byte(descriptor)
			if _, err := FromFiles("example", files); err == nil {
				t.Fatal("accepted invalid descriptor")
			}
		})
	}
	for _, extra := range []string{"../secret", "a\\b", "/abs", "nul.txt", "a:b", "a. /b", "MAIN.py", "main.py/child", "Assets/other.bin"} {
		t.Run(extra, func(t *testing.T) {
			files := sampleFiles()
			files[extra] = []byte("invalid")
			if _, err := FromFiles("example", files); err == nil {
				t.Fatal("accepted invalid file path/collision")
			}
		})
	}
	files := sampleFiles()
	files["big"] = make([]byte, MaxFileBytes+1)
	if _, err := FromFiles("example", files); err == nil {
		t.Fatal("accepted oversized file")
	}
	files = sampleFiles()
	resource := make([]byte, 7<<20)
	for _, name := range []string{"a.bin", "b.bin", "c.bin"} {
		files[name] = resource
	}
	if _, err := FromFiles("example", files); err == nil {
		t.Fatal("accepted oversized folder")
	}
	files = sampleFiles()
	for i := 0; i < MaxFiles; i++ {
		files[fmt.Sprintf("file%d", i)] = nil
	}
	if _, err := FromFiles("example", files); err == nil {
		t.Fatal("accepted too many files")
	}
}

func TestLoadAndLinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plugins", "example")
	for rel, data := range sampleFiles() {
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	def, files, err := Load(root, "example")
	if err != nil || def.Name != "example" || len(files) != 4 {
		t.Fatalf("load: %v, %v", def, err)
	}
	if _, _, err := Load(root, "../escape"); err == nil {
		t.Fatal("accepted invalid name")
	}
	if _, _, err := Load("", "example"); err == nil {
		t.Fatal("accepted empty root")
	}
	target := filepath.Join(dir, "link.py")
	if err := os.Symlink(filepath.Join(dir, "main.py"), target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := Load(root, "example"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("load link: %v", err)
	}
	if err := CheckTarget(root, "plugins/example/link.py"); err == nil {
		t.Fatal("accepted linked destination")
	}
}
