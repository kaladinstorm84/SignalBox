package knowledgebase

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxDocumentBytes = 1 << 20
	MaxContextBytes  = 5 << 20
)

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	textExts    = map[string]bool{
		".csv": true, ".json": true, ".log": true, ".md": true, ".txt": true,
		".tsv": true, ".xml": true, ".yaml": true, ".yml": true,
	}
)

type Store struct {
	root string
}

type Base struct {
	Name      string     `json:"name"`
	Documents []Document `json:"documents"`
}

type Document struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

func New(projectRoot string) *Store {
	return &Store{root: filepath.Join(projectRoot, "knowledge-bases")}
}

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

func ValidDocumentName(name string) bool {
	return name != "" && filepath.Base(name) == name && !strings.ContainsAny(name, `/\`) &&
		textExts[strings.ToLower(filepath.Ext(name))]
}

func (s *Store) List() ([]Base, error) {
	if err := s.ensureRoot(false); os.IsNotExist(err) {
		return []Base{}, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	bases := make([]Base, 0, len(entries))
	for _, entry := range entries {
		if !ValidName(entry.Name()) || !entry.IsDir() {
			continue
		}
		docs, err := s.documents(entry.Name())
		if err != nil {
			return nil, err
		}
		bases = append(bases, Base{Name: entry.Name(), Documents: docs})
	}
	return bases, nil
}

func (s *Store) Create(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("knowledge base name must start with a letter or digit and contain only letters, digits, _ or - (up to 64 characters)")
	}
	if err := s.ensureRoot(true); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(s.root, name), 0o755); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("knowledge base %q already exists", name)
		}
		return err
	}
	return nil
}

func (s *Store) Delete(name string) error {
	dir, err := s.baseDir(name)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("knowledge base %q does not exist", name)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("knowledge base %q is not a regular directory", name)
	}
	return os.RemoveAll(dir)
}

func (s *Store) SaveDocument(base, name string, content []byte) error {
	path, err := s.documentPath(base, name)
	if err != nil {
		return err
	}
	if len(content) > MaxDocumentBytes {
		return fmt.Errorf("document exceeds the %d-byte limit", MaxDocumentBytes)
	}
	if !utf8.Valid(content) {
		return fmt.Errorf("document must contain UTF-8 text")
	}
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("knowledge base %q does not exist", base)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("knowledge base %q is not a regular directory", base)
	}
	if existing, err := os.Lstat(path); err == nil {
		if !existing.Mode().IsRegular() || existing.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("document %q is not a regular file", name)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	return nil
}

func (s *Store) DeleteDocument(base, name string) error {
	path, err := s.documentPath(base, name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	baseInfo, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("knowledge base %q does not exist", base)
	}
	if err != nil {
		return err
	}
	if !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("knowledge base %q is not a regular directory", base)
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("document %q does not exist in knowledge base %q", name, base)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("document %q is not a regular file", name)
	}
	return os.Remove(path)
}

func (s *Store) Content(names []string) (string, error) {
	var content strings.Builder
	for _, name := range names {
		dir, err := s.baseDir(name)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return "", fmt.Errorf("knowledge base %q does not exist", name)
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("knowledge base %q is not a regular directory", name)
		}
		docs, err := s.documents(name)
		if err != nil {
			return "", err
		}
		for _, doc := range docs {
			path := filepath.Join(dir, doc.Name)
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("reading %s/%s: %w", name, doc.Name, err)
			}
			if !utf8.Valid(data) {
				return "", fmt.Errorf("document %s/%s is not UTF-8 text", name, doc.Name)
			}
			header := fmt.Sprintf("\n\n--- Knowledge base: %s / %s ---\n", name, doc.Name)
			if content.Len()+len(header)+len(data) > MaxContextBytes {
				return "", fmt.Errorf("attached knowledge exceeds the %d-byte context limit", MaxContextBytes)
			}
			content.WriteString(header)
			content.Write(data)
		}
	}
	return content.String(), nil
}

func (s *Store) documents(base string) ([]Document, error) {
	dir, err := s.baseDir(base)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	docs := make([]Document, 0, len(entries))
	for _, entry := range entries {
		if !ValidDocumentName(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		docs = append(docs, Document{Name: entry.Name(), Size: int(info.Size())})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs, nil
}

func (s *Store) baseDir(name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid knowledge base name %q", name)
	}
	if err := s.ensureRoot(false); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
	}
	return filepath.Join(s.root, name), nil
}

func (s *Store) documentPath(base, name string) (string, error) {
	if !ValidDocumentName(name) {
		return "", fmt.Errorf("document name must be a plain-text file (txt, md, csv, tsv, json, yaml, yml, xml or log)")
	}
	dir, err := s.baseDir(base)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func (s *Store) ensureRoot(create bool) error {
	info, err := os.Lstat(s.root)
	if os.IsNotExist(err) && create {
		if err := os.MkdirAll(s.root, 0o755); err != nil {
			return err
		}
		info, err = os.Lstat(s.root)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("knowledge-bases path is not a regular directory")
	}
	return nil
}
