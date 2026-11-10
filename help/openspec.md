# OpenSpec integration

[Back to SignalBox](../README.md)

### OpenSpec integration

[OpenSpec](https://github.com/Fission-AI/OpenSpec) manages specification-driven
change workflows; it is not an AI model runtime. Use **`type: openspec`** for
CLI operations and normal agents for writing artifacts or implementing tasks.
No `runtime: openspec` entry is needed.

```yaml
- id: implementation_instructions
  type: openspec
  timeout_seconds: 60
  openspec:
    args: [instructions, apply, --change, "${{ params.change }}", --json]
  handoff: [developer]
```

Install the OpenSpec CLI separately (see its
[CLI reference](https://github.com/Fission-AI/OpenSpec/blob/main/docs/cli.md))
and initialize OpenSpec in the SignalBox project directory before running a
workflow. `openspec` must be on the server's `PATH`. Optionally set
`openspec.command` to an executable name or absolute path. For example, to
invoke a JavaScript CLI entry point directly, use `command: node` and put
the entry-point path first in `args`.
On Windows, standard global and local npm `openspec.cmd` installations are
launched through their Node.js entry point automatically, without a shell.
For other shim layouts, configure `command: node` and the entry-point path
explicitly; SignalBox reports an actionable error rather than shell-parsing
your arguments.

- `openspec.args` is a required non-empty argument list. Each item supports
  `${{ params.name }}` substitution and remains one argument, without shell
  parsing. The executable itself is not parameterized.
- Commands run in the directory containing `signalbox.yaml`, with the server
  user's permissions, not in a sandbox. Only run trusted pipeline definitions.
  Incoming handoff text is **not** appended as an argument or sent on stdin.
- Use explicit change names and non-interactive flags where supported:
  `status --change <name> --json`, `instructions <artifact> --change <name> --json`,
  or `validate <name> --type change --strict --no-interactive --json`.
  SignalBox does not add confirmation flags or automatically initialize,
  implement, or archive changes.
- Stdout is the node output; nonzero exits, missing executables, and timeouts
  use the normal retry/error handling, including stderr diagnostics. These
  nodes share the runtime concurrency cap, cancellation, and interrupted-run
  restart handling. The default timeout is 600 seconds. Retrying a mutating
  command can repeat side effects; use automatic `retry` only when safe.
- To preserve incoming requirements through a CLI step, use a handoff `input`
  template combining `{{ input }}` and `{{ output }}`. Agents can also consume
  completed OpenSpec outputs through execution `context`.
- AI-only settings (`runtime`, `prompt`, `model`, `mcp`, `knowledge_bases`),
  context consumption, file delivery, and `output.schema` are not supported on
  OpenSpec command nodes. Use a downstream agent or code node when needed.

**Example:** [openspec-change.yaml](../testproject/pipelines/openspec-change.yaml)
creates a new `spec-driven` change, obtains CLI instructions for proposal,
specs, design and tasks, and has Codex write each artifact. It validates the
planning pack, pauses for human approval, obtains `apply` instructions, then
implements and validates again. A clean pass makes **five LLM calls**.
Rejection revises planning without recreating the change. The final validation
checks OpenSpec artifacts; it is not a substitute for application tests.

From `testproject`, with OpenSpec initialized and the configured Codex runtime
allowed to write workspace files:

```powershell
..\signalbox.exe run openspec-change --param change=add-export --input "Add CSV export"
```

Use a new kebab-case change name for each execution. Do not pass `--yes`, which
bypasses human approvals. Planning agents are instructed to write only change
artifacts before approval; this is a prompt-level contract, not a filesystem
sandbox. Review the actual files as well as the planning summary. Changes are
left active for review; archiving remains an explicit separate action.
