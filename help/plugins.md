# Script plugins

[Back to SignalBox](../README.md)

Plugins package reusable code as named workflow node types. A plugin is a
folder under `plugins/<name>/`, containing `plugin.yaml`, a script entry point,
and any helper files or resources it needs. It can be shared independently
through the [registry](registry.md), or bundled with a published pipeline.

## Define a plugin

```text
plugins/
  invoice-check/
    plugin.yaml
    main.py
    invoice_rules.py
    resources/
      rules.json
```

```yaml
# plugins/invoice-check/plugin.yaml
description: Validate an invoice supplied as JSON.
language: python
entry: main.py
timeout_seconds: 30
codes:
  success: 0
  fail: 1
  retry: 2
```

Supported languages are `python`, `javascript` (Node.js), and `powershell`.
Install the corresponding interpreter (`python`, `node`, or `powershell`) on
`PATH`, along with any dependencies. SignalBox does not install dependencies
or run installation hooks.

`entry` must name a regular file inside the plugin folder. `timeout_seconds`
defaults to 600; `codes` is optional and has the same
[exit/rework semantics as code steps](code-steps.md). Unknown descriptor
fields are rejected. The folder name is the plugin's name, not a descriptor
field. Names use lowercase letters, digits, `_` and `-`, starting with a
letter or digit, up to 64 characters; Windows device names are forbidden.

## Use it in a workflow

```yaml
name: validate-invoice
entry: validate
agents:
  - id: validate
    type: plugin:invoice-check
    retry: {count: 1, backoff: 500ms}
```

Use the node in handoffs and orchestrator stages just like `type: code`.
The fully assembled handoff is sent to **stdin**. Write the result to
**stdout**, and diagnostics to **stderr**. This first version has no plugin
parameters: put invocation-specific data in the handoff input.

The descriptor owns the language, entry, timeout and exit mappings; do not
repeat these fields on the workflow node. Runtime/model settings, AI retry
fallback sequences, prompts and stages do not apply. Fixed retries, context,
additional input, `files: path`, output schemas and handoff routing work as
they do for code nodes. Plugin output schemas default to failing invalid
output rather than asking an AI to repair it.

Scripts run with the **project root as their working directory**, not the
plugin folder. Resolve plugin resources relative to the entry script:
`Path(__file__).resolve().parent` in Python, `__dirname` in CommonJS JavaScript
(`import.meta.url` for ES modules), or `$PSScriptRoot` in PowerShell.
Keep generated output outside the plugin folder. Python plugins disable
automatic bytecode-cache writes.

The whole folder's content digest is included in the pipeline definition
fingerprint. Changed helpers or resources therefore trigger the normal
definition-change warning on restart. If a folder changes after a definition
was loaded, execution requires a reload rather than silently using changed
files.

## Publish and install

Configure a directory registry as described in the [registry guide](registry.md).

```powershell
signalbox registry publish-plugin invoice-check --version 1.0.0
signalbox registry show invoice-check@1.0.0
signalbox registry install invoice-check@1.0.0
```

Without `--version`, publishing starts at `0.1.0` and increments the latest
patch version. Versions are immutable. The web UI provides the same actions
under **Registry > Plugins**, including descriptor/script previews. Binary
and large resources are listed but not displayed as text.

The registry stores a `kind: plugin` package and installs it into
`plugins/<name>/`. All regular files, including helper scripts, dotfiles and
binary resources, are included: **do not put credentials, caches, virtual
environments or private files in a plugin folder**. Empty directories are
not packaged. Limits per plugin are 1,000 files, 10 MiB per file and 20 MiB
total. Paths must be portable; traversal, symbolic links, special files and
case-insensitive path collisions are refused.

Publishing a pipeline includes the full folders of every referenced plugin,
including plugins used by imported agents. A separate plugin install is not
required for that pipeline. Pipeline, knowledge and plugin packages share
one registry name namespace.

Installation verifies hashes/digests, records files in `signalbox.lock.json`,
and protects local edits. When upgrading a plugin folder, tracked obsolete
files are removed if unchanged; `--force` also permits removing locally edited
obsolete files. Untracked extra files must be moved or removed manually, even
with `--force`. An obsolete file still tracked by another package blocks the
upgrade until the shared versions are reconciled. This keeps the installed
folder's behavior consistent with the inspected package. As with other
registry installs, writes/removals are per-file, not a whole-project transaction.

**Trust:** publish, inspect and install never execute plugin code.
Running a plugin executes it with your user permissions, **not in a sandbox**.
Review code from standalone packages and bundled pipeline plugins before
running them. A digest verifies integrity, not trustworthiness.

## Runnable example

The sample project includes [text-stats](../testproject/plugins/text-stats/)
with a separate helper module, and a
[plugin-text-stats pipeline](../testproject/pipelines/plugin-text-stats.yaml).
With Python installed, run from the repository root:

```powershell
Set-Location testproject
go run ..\cmd\signalbox run plugin-text-stats --input "Hello reusable plugins"
```

The result is JSON containing `lines`, `words` and `characters`. No AI calls
or credentials are required. You can also launch it from the web UI.
