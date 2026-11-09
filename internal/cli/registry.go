package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/registry"
)

const registryUsage = `Usage:
  signalbox registry search [query]
  signalbox registry versions <name>
  signalbox registry show <name[@version]> [--file path]
  signalbox registry publish <pipeline> [--version x.y.z] [--by name]
  signalbox registry publish-knowledge <base> [--version x.y.z] [--description text] [--by name]
  signalbox registry install <name[@version]> [--digest sha256:...] [--force]
  signalbox registry installed

Flags:
  --registry path    Registry directory (default: registry.path in signalbox.yaml)

The registry shares pipeline and knowledge-base packages through a directory
or file share. Published versions are immutable; installs verify every file's
sha256 and the package digest, validate pipelines against this project's
runtimes, and record the result in
signalbox.lock.json.
`

// cmdRegistry works directly on the registry directory and the project in
// the current directory.
func cmdRegistry(args []string, out io.Writer) error {
	a, err := parseOpsArgs(args, []string{"registry", "file", "version", "by", "digest", "description"}, []string{"force"})
	if err != nil {
		return err
	}
	if len(a.positional) == 0 {
		return fmt.Errorf("missing registry command\n\n%s", registryUsage)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}
	sub, rest := a.positional[0], a.positional[1:]
	if sub == "installed" {
		return registryInstalled(cfg, out)
	}
	dir := cfg.RegistryPath()
	if flag := a.value("registry"); flag != "" {
		dir = config.ResolveRegistryPath(cwd, flag)
	}
	reg, err := registry.Open(dir)
	if err != nil {
		return err
	}
	need := func(n int, usage string) error {
		if len(rest) != n {
			return fmt.Errorf("usage: signalbox registry %s", usage)
		}
		return nil
	}

	switch sub {
	case "search":
		results, err := reg.Search(strings.Join(rest, " "))
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Fprintln(out, "no packages found")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tKIND\tLATEST\tVERSIONS\tDESCRIPTION")
		for _, s := range results {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", s.Name, s.Kind, s.Latest, len(s.Versions), oneLine(s.Description, 70))
		}
		return w.Flush()
	case "versions":
		if err := need(1, "versions <name>"); err != nil {
			return err
		}
		versions, err := reg.Versions(rest[0])
		if err != nil {
			return err
		}
		for _, v := range versions {
			m, err := reg.Manifest(rest[0], v)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\t%s\t%s\n", v, m.PublishedAt.Local().Format("2006-01-02 15:04"), m.Digest)
		}
	case "show":
		if err := need(1, "show <name[@version]> [--file path]"); err != nil {
			return err
		}
		name, version, err := reg.Resolve(rest[0])
		if err != nil {
			return err
		}
		m, err := reg.Manifest(name, version)
		if err != nil {
			return err
		}
		if file := a.value("file"); file != "" {
			data, err := reg.ReadFile(m, file)
			if err != nil {
				return err
			}
			_, err = out.Write(data)
			return err
		}
		fmt.Fprintf(out, "%s@%s\n", m.Name, m.Version)
		fmt.Fprintf(out, "Kind: %s\n", m.PackageKind())
		if m.Description != "" {
			fmt.Fprintf(out, "%s\n", strings.TrimSpace(m.Description))
		}
		fmt.Fprintf(out, "\nPublished: %s", m.PublishedAt.Local().Format("2006-01-02 15:04"))
		if m.PublishedBy != "" {
			fmt.Fprintf(out, " by %s", m.PublishedBy)
		}
		fmt.Fprintf(out, "\nSchema:    %d\nDigest:    %s\nEntry:     %s\n", m.SchemaVersion, m.Digest, m.Entry)
		if len(m.Runtimes) > 0 {
			fmt.Fprintf(out, "Runtimes:  %s\n", strings.Join(m.Runtimes, ", "))
		}
		if len(m.Params) > 0 {
			fmt.Fprintf(out, "Params:    %s\n", strings.Join(m.Params, ", "))
		}
		fmt.Fprintln(out, "Files:")
		for _, f := range m.Files {
			fmt.Fprintf(out, "  %s  %6d  %s\n", f.SHA256[:12], f.Size, f.Path)
		}
	case "publish", "publish-knowledge":
		if err := need(1, sub+" <name> [--version x.y.z]"); err != nil {
			return err
		}
		by := a.value("by")
		if by == "" {
			by = cliUser()
		}
		opts := registry.PublishOptions{Version: a.value("version"), By: by}
		var m *registry.Manifest
		if sub == "publish-knowledge" {
			m, err = reg.PublishKnowledge(cfg, rest[0], a.value("description"), opts)
		} else {
			m, err = reg.Publish(cfg, rest[0], opts)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "published %s@%s (%d files, %s)\n", m.Name, m.Version, len(m.Files), m.Digest)
	case "install":
		if err := need(1, "install <name[@version]> [--digest sha256:...] [--force]"); err != nil {
			return err
		}
		result, err := reg.Install(cfg, rest[0], registry.InstallOptions{Digest: a.value("digest"), Force: a.switches["force"]})
		if err != nil {
			return err
		}
		m := result.Manifest
		fmt.Fprintf(out, "installed %s@%s (%s)\n", m.Name, m.Version, m.Digest)
		for _, f := range result.Written {
			fmt.Fprintf(out, "  added     %s\n", f)
		}
		for _, f := range result.Replaced {
			fmt.Fprintf(out, "  replaced  %s\n", f)
		}
		for _, f := range result.Skipped {
			fmt.Fprintf(out, "  unchanged %s\n", f)
		}
	default:
		return fmt.Errorf("unknown registry command %q\n\n%s", sub, registryUsage)
	}
	return nil
}

func registryInstalled(cfg *config.Config, out io.Writer) error {
	lock, err := registry.ReadLock(cfg.Root)
	if err != nil {
		return err
	}
	if len(lock.Packages) == 0 {
		fmt.Fprintln(out, "no packages installed")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tVERSION\tINSTALLED\tDIGEST")
	for _, name := range sortedNames(lock.Packages) {
		e := lock.Packages[name]
		kind := e.Kind
		if kind == "" {
			kind = registry.KindPipeline
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, kind, e.Version, e.InstalledAt.Local().Format("2006-01-02 15:04"), e.Digest)
	}
	return w.Flush()
}

func sortedNames(m map[string]registry.LockEntry) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
