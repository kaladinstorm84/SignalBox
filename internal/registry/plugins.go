package registry

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/plugin"
)

func (r *Registry) PublishPlugin(cfg *config.Config, name string, opts PublishOptions) (*Manifest, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid plugin package name %q", name)
	}
	if err := r.checkNameClash(name); err != nil {
		return nil, err
	}
	def, files, err := plugin.Load(cfg.Root, name)
	if err != nil {
		return nil, err
	}
	version := opts.Version
	versions, err := r.pluginVersions(name)
	if err != nil {
		return nil, err
	}
	if version == "" {
		version = "0.1.0"
		if len(versions) > 0 {
			version = pipeline.NextPatch(versions[0])
		}
	}
	if !pipeline.ValidVersion(version) {
		return nil, fmt.Errorf("version %q must be a semantic version such as 1.2.0", version)
	}
	for _, v := range versions {
		if v == version {
			return nil, fmt.Errorf("%s@%s is already published; versions are immutable", name, version)
		}
	}
	contents := map[string][]byte{}
	for rel, data := range files {
		contents[path.Join("plugins", name, rel)] = data
	}
	return r.publishContents(&Manifest{
		Kind: KindPlugin, Name: name, Version: version, Description: def.Description,
		SchemaVersion: pipeline.SchemaVersion, PublishedAt: time.Now().UTC().Truncate(time.Second),
		PublishedBy: opts.By, Entry: path.Join("plugins", name, plugin.Descriptor),
	}, contents)
}

func (r *Registry) pluginVersions(name string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(r.Dir, name)); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	versions, err := r.Versions(name)
	if err != nil {
		return nil, err
	}
	previous, err := r.Manifest(name, versions[0])
	if err != nil {
		return nil, err
	}
	if previous.PackageKind() != KindPlugin {
		return nil, fmt.Errorf("package %q is already used by a %s; choose a different name", name, kindLabel(previous.PackageKind()))
	}
	return versions, nil
}

func kindLabel(kind string) string {
	if kind == KindKnowledge {
		return "knowledge base"
	}
	return kind
}

// validatePluginFolders also checks plugins bundled in pipeline packages.
func validatePluginFolders(m *Manifest, contents map[string][]byte) error {
	folders := map[string]map[string][]byte{}
	for rel, data := range contents {
		if !strings.HasPrefix(rel, "plugins/") {
			continue
		}

		parts := strings.SplitN(rel, "/", 3)
		if len(parts) != 3 || !plugin.ValidName(parts[1]) {
			return fmt.Errorf("invalid plugin package path %q", rel)
		}
		name := parts[1]
		if folders[name] == nil {
			folders[name] = map[string][]byte{}
		}
		folders[name][parts[2]] = data
	}
	if m.PackageKind() == KindPlugin {
		if len(folders) != 1 || folders[m.Name] == nil || m.Entry != path.Join("plugins", m.Name, plugin.Descriptor) {
			return fmt.Errorf("plugin package %s@%s must contain plugins/%s/%s", m.Name, m.Version, m.Name, plugin.Descriptor)
		}
		for rel := range contents {
			if !strings.HasPrefix(rel, "plugins/"+m.Name+"/") {
				return fmt.Errorf("plugin package may only contain files in plugins/%s/", m.Name)
			}
		}
	}
	for name, files := range folders {
		if _, err := plugin.FromFiles(name, files); err != nil {
			return err
		}
	}
	return nil
}

// pluginRemovals makes installed folders match their package exactly, without
// deleting untracked files or files still owned by another installed package.
func pluginRemovals(root, packageName string, targets, installed map[string]string, lock *Lock, force bool) ([]string, error) {
	expected := map[string]bool{}
	folders := map[string]bool{}
	for _, target := range targets {
		if !strings.HasPrefix(target, "plugins/") {
			continue
		}
		parts := strings.SplitN(target, "/", 3)
		folders[path.Join(parts[0], parts[1])] = true
		expected[target] = true
	}
	var removed []string
	for folder := range folders {
		if err := plugin.CheckTarget(root, folder); err != nil {
			return nil, err
		}
		dir := filepath.Join(root, filepath.FromSlash(folder))
		err := filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) && file == dir {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("plugin path %s must not be a symbolic link", file)
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("plugin path %s must be a regular file", file)
			}
			rel, err := filepath.Rel(root, file)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if expected[rel] {
				return nil
			}
			previous, tracked := installed[rel]
			if !tracked {
				return fmt.Errorf("untracked plugin file %s is not in this package; move or remove it before installing (even with --force)", rel)
			}
			for owner, pkg := range lock.Packages {
				if owner == packageName {
					continue
				}
				for _, f := range pkg.Files {
					if f.Path == rel {
						return fmt.Errorf("obsolete plugin file %s is also tracked by package %s; resolve the shared plugin versions before installing", rel, owner)
					}
				}
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if !force && hashBytes(data) != previous {
				return fmt.Errorf("obsolete plugin file %s has local edits (pass --force to remove)", rel)
			}
			removed = append(removed, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(removed)
	return removed, nil
}
