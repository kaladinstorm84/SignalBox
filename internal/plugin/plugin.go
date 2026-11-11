// Package plugin describes folder-backed script node types. Loading and
// packaging inspect files only; code runs exclusively through the code runtime.
package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	Descriptor      = "plugin.yaml"
	MaxFiles        = 1000
	MaxFileBytes    = 10 << 20
	MaxPackageBytes = 20 << 20
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Definition is the snapshotted execution contract of one installed plugin.
type Definition struct {
	Name           string         `yaml:"-" json:"name"`
	Description    string         `yaml:"description,omitempty" json:"description,omitempty"`
	Language       string         `yaml:"language" json:"language"`
	Entry          string         `yaml:"entry" json:"entry"`
	TimeoutSeconds int            `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	Codes          map[string]int `yaml:"codes,omitempty" json:"codes,omitempty"`
	Digest         string         `yaml:"-" json:"digest"`
}

func ValidName(name string) bool {
	if !namePattern.MatchString(name) {
		return false
	}
	switch name {
	case "con", "prn", "aux", "nul":
		return false
	}
	return !(len(name) == 4 && (strings.HasPrefix(name, "com") || strings.HasPrefix(name, "lpt")) && name[3] >= '1' && name[3] <= '9')
}

// ValidPath requires portable, canonical, folder-relative file paths.
func ValidPath(rel string) bool {
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || strings.ContainsAny(rel, `\:<>"|?*`+"\x00") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "." || part == ".." || strings.TrimRight(part, ". ") != part {
			return false
		}
		stem := strings.ToLower(strings.SplitN(part, ".", 2)[0])
		if stem == "con" || stem == "prn" || stem == "aux" || stem == "nul" ||
			len(stem) == 4 && (strings.HasPrefix(stem, "com") || strings.HasPrefix(stem, "lpt")) && stem[3] >= '1' && stem[3] <= '9' {
			return false
		}
		for _, c := range part {
			if c < 32 {
				return false
			}
		}
	}
	return true
}

// FromFiles validates a complete plugin folder without executing it.
func FromFiles(name string, files map[string][]byte) (*Definition, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid plugin name %q", name)
	}
	if len(files) == 0 || len(files) > MaxFiles {
		return nil, fmt.Errorf("plugin %q must contain 1-%d files", name, MaxFiles)
	}
	data, ok := files[Descriptor]
	if !ok {
		return nil, fmt.Errorf("plugin %q is missing %s", name, Descriptor)
	}
	var def Definition
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&def); err != nil {
		return nil, fmt.Errorf("plugin %q: %w", name, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return nil, fmt.Errorf("plugin %q: descriptor must contain one YAML document", name)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("plugin %q: %w", name, err)
	}
	switch def.Language {
	case "python", "javascript", "powershell":
	default:
		return nil, fmt.Errorf("plugin %q: language must be python, javascript, or powershell", name)
	}
	if !ValidPath(def.Entry) || def.Entry == Descriptor {
		return nil, fmt.Errorf("plugin %q: entry must be a portable script path inside its folder", name)
	}
	if _, ok := files[def.Entry]; !ok {
		return nil, fmt.Errorf("plugin %q: entry %q is missing", name, def.Entry)
	}
	if def.TimeoutSeconds < 0 {
		return nil, fmt.Errorf("plugin %q: timeout_seconds must not be negative", name)
	}
	if err := ValidateCodes(def.Codes); err != nil {
		return nil, fmt.Errorf("plugin %q: %w", name, err)
	}
	var names []string
	seen := map[string]bool{}
	components := map[string]string{}
	total := 0
	for rel, data := range files {
		if !ValidPath(rel) {
			return nil, fmt.Errorf("plugin %q: unsafe file path %q", name, rel)
		}
		key := strings.ToLower(rel)
		if seen[key] {
			return nil, fmt.Errorf("plugin %q: case-insensitive file collision %q", name, rel)
		}
		seen[key] = true
		for component := rel; component != "."; component = path.Dir(component) {
			key := strings.ToLower(component)
			if previous, ok := components[key]; ok && previous != component {
				return nil, fmt.Errorf("plugin %q: case-insensitive path collision %q and %q", name, previous, component)
			}
			components[key] = component
		}
		if len(data) > MaxFileBytes {
			return nil, fmt.Errorf("plugin %q: file %q exceeds %d bytes", name, rel, MaxFileBytes)
		}
		total += len(data)
		if total > MaxPackageBytes {
			return nil, fmt.Errorf("plugin %q exceeds %d bytes", name, MaxPackageBytes)
		}
		names = append(names, rel)
	}
	for rel := range files {
		for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
			if seen[strings.ToLower(parent)] {
				return nil, fmt.Errorf("plugin %q: file/directory collision at %q", name, parent)
			}
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, rel := range names {
		sum := sha256.Sum256(files[rel])
		fmt.Fprintf(h, "%s:%x\n", rel, sum)
	}
	def.Name, def.Digest = name, "sha256:"+hex.EncodeToString(h.Sum(nil))
	return &def, nil
}

// ValidateCodes is shared by raw code nodes and plugin descriptors.
func ValidateCodes(codes map[string]int) error {
	if codes == nil {
		return nil
	}
	if len(codes) != 3 {
		return fmt.Errorf("codes must define success, fail, and retry")
	}
	seen := map[int]bool{}
	for _, outcome := range []string{"success", "fail", "retry"} {
		code, ok := codes[outcome]
		if !ok || code < 0 {
			return fmt.Errorf("codes.%s must be a non-negative exit code", outcome)
		}
		if seen[code] {
			return fmt.Errorf("exit codes must be distinct")
		}
		seen[code] = true
	}
	return nil
}

// CheckTarget rejects links in every existing component, including the root.
func CheckTarget(root, rel string) error {
	current := root
	parts := append([]string{""}, strings.Split(rel, "/")...)
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin path %s must not be a symbolic link", current)
		}
	}
	return nil
}

// Load reads all regular files under plugins/name. Links and special files are
// rejected rather than traversed; interpreters and dependencies are not invoked.
func Load(root, name string) (*Definition, map[string][]byte, error) {
	if root == "" {
		return nil, nil, fmt.Errorf("plugin %q requires a project root", name)
	}
	if !ValidName(name) {
		return nil, nil, fmt.Errorf("invalid plugin name %q", name)
	}
	rel := path.Join("plugins", name)
	if err := CheckTarget(root, rel); err != nil {
		return nil, nil, err
	}
	dir := filepath.Join(root, "plugins", name)
	files := map[string][]byte{}
	total := int64(0)
	err := filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin %q contains symbolic link %s", name, file)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("plugin %q contains non-regular file %s", name, file)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if info.Size() > MaxFileBytes || total > MaxPackageBytes || len(files) >= MaxFiles {
			return fmt.Errorf("plugin %q exceeds file or package limits", name)
		}
		relative, err := filepath.Rel(dir, file)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("loading plugin %q: %w", name, err)
	}
	def, err := FromFiles(name, files)
	return def, files, err
}

func List(root string) ([]string, error) {
	if err := CheckTarget(root, "plugins"); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, "plugins"))
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !ValidName(entry.Name()) {
			continue
		}
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		if _, _, err := Load(root, entry.Name()); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	return names, nil
}
