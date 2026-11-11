# Pipeline, plugin and knowledge registry

[Back to SignalBox](../README.md)

## Pipeline registry

A registry is a directory, usually on a file share, holding versioned,
hash-verified pipeline, plugin and knowledge-base packages. Configure it in `signalbox.yaml`:

```yaml
registry:
  path: ./registry      # relative to the project, absolute, or file:///S:/team/registry
```

```text
signalbox registry search [query]
signalbox registry versions <name>
signalbox registry show <name>[@version] [--file path]
signalbox registry publish <pipeline> [--version x.y.z] [--by name]
signalbox registry publish-knowledge <base> [--version x.y.z] [--description text] [--by name]
signalbox registry publish-plugin <name> [--version x.y.z] [--by name]
signalbox registry install <name>[@version|@latest] [--digest sha256:...] [--force]
signalbox registry installed
```

Every command accepts `--registry PATH` to override the configured path. The
**Registry** page in the web UI has separate **Pipelines**, **Plugins** and **Knowledge**
tabs, each offering search, inspect, version selection, install, and publish
actions. The Knowledge tab lists non-empty local knowledge bases for publishing;
create or upload documents on the **Knowledge bases** page first.

- **Packages.** Publishing validates the pipeline and packages it together
  with its imports, the scripts its code steps use, complete referenced plugin folders, the schema files it
  references, and the documents in its attached knowledge bases. Publishing
  a knowledge base packages all its supported UTF-8 text documents, without
  requiring a pipeline or a particular runtime. Files outside the
  project are refused. The layout is
  `<registry>/<name>/<version>/manifest.json` plus `files/<project path>`. The
  manifest lists each file's SHA-256 and a package digest, along with the
  runtimes and params the pipeline needs. New manifests declare `kind` as
  `pipeline`, `plugin` or `knowledge`; older manifests without a kind remain pipelines.
  [Plugin packages](plugins.md) contain all regular files in `plugins/<name>/`,
  including their descriptor, scripts, helpers and binary resources.
- **Names.** The package name is the pipeline, plugin or knowledge-base name. All
  package types share a registry namespace, so a name cannot change type
  between versions. It must be lowercase
  letters, digits, `_`, or `-` (up to 64 characters, starting with a letter or
  digit), so packages behave the same on case-insensitive file systems and
  shares. Windows device names (`con`, `nul`, `com1`, …) are refused, as is
  a name differing only in case from an existing package.
- **Versions.** These are semantic versions (`1.2.0`, `2.0.0-rc.1`). The
  version comes from `--version`, else the pipeline's `version:` field, else
  the next patch after the latest published version (`0.1.0` first).
  Knowledge bases and plugins use `--version` or the next patch (`0.1.0` first).
  Published versions are immutable.
- **Integrity.** `show` and `install` verify every file hash and the digest.
  Install can be pinned with `--digest`; the UI always pins to the digest it
  displayed. A package written for a newer pipeline schema (`schema:`) is
  refused with a request to upgrade SignalBox.
- **Install.** Files land in this project, with the package's pipeline mapped
  into this project's `pipelines_dir` and its schemas into `schemas_dir`. The pipeline is validated against this
  project's runtimes in a staging copy before anything is written.
  Installation is refused when a different pipeline already uses the name, or
  when an existing file differs from the package. Files unchanged since the
  last install upgrade freely; `--force` overwrites local edits. Installed
  versions and file hashes are recorded in `signalbox.lock.json`.
  Knowledge packages install documents into `knowledge-bases/<name>/` with
  the same digest verification, upgrade tracking, and local-edit protection.
  Attach an installed base to an agent with `knowledge_bases: [name]`.
  Plugin packages install into `plugins/<name>/`; use `type: plugin:<name>`.
  Plugin upgrades remove unchanged tracked obsolete files, protect local edits,
  and refuse untracked extras. See the [plugin upgrade rules](plugins.md#publish-and-install).
  Publish/install never run code; plugin execution is **not sandboxed**.
  Local pipeline cards and details, and knowledge-base cards, show a
  **From registry** marker with the last installed package version.
  Knowledge bundled with a pipeline identifies that pipeline package and its
  version. These markers come from `signalbox.lock.json`; they record the
  installed version, not a guarantee that local files remain unedited.
- **Git.** Keep a `.gitattributes` containing `* -text` in a registry that
  lives in git, so line-ending conversion does not change the hashed bytes.

The test project's `registry/` holds `release-notes` 1.0.0 and 1.1.0. Install
1.0.0, then upgrade to 1.1.0 to see the lock file track it.
