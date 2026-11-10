# Project configuration and watchers

[Back to SignalBox](../README.md)

## Project configuration (`signalbox.yaml`)

```yaml
version: 1
project: my-project
pipelines_dir: pipelines
schemas_dir: schemas            # JSON Schema files; see "Structured responses"
state_dir: .signalbox
execution_retention: 1000
max_concurrent_calls: 4
server:
  host: 127.0.0.1
  port: 8484
runtimes:
  mock:
    type: mock
  openai:
    type: openai
    model: gpt-4o
    api_key_env: OPENAI_API_KEY
  claude:
    type: claude          # claude -p "<prompt>"
  codex:
    type: codex           # codex exec "<prompt>"
  copilot:
    type: copilot         # copilot -p "<prompt>"
  custom:
    type: cli
    command: my-agent
    model_arg: --model    # lets agents set model: on a cli runtime
allow_external_imports: false   # imports must stay inside the project
mcp:                            # see "MCP servers"
  enabled: false
  servers: {}
registry:                       # see "Pipeline registry"
  path: ./registry
uploads:                        # see "File uploads"
  max_bytes: 10485760           # per file (max 1 GiB)
  max_files: 10                 # per execution (max 100)
  allowed_extensions: [.txt, .md, .csv, .json, .pdf, .png]
```

`max_concurrent_calls` limits simultaneous runtime invocations across all
executions sharing a project's state database, including detached workers,
foreground runs, child pipelines, code steps and orchestrator synthesis.
Calls above the limit wait for a shared SQLite slot and stop waiting if their
execution is canceled. Approval, manual-edit and manual-retry waits do not
occupy a call slot. Omitted or `0` selects the default of `4`; negative values
are invalid. Restart the server after changing the cap. Lowering it does not
interrupt existing calls; new calls wait until usage drops below the cap.
Different state directories have independent limits.

The execution list API is paginated with `page` (1-based) and `page_size`
(10–100, in increments of 10); the web UI defaults to 20 rows per page and
remembers the selected size in that browser.

### Watchers

Watchers are project-level pollers started by `signalbox serve`. Each runs a
command directly (without a shell) at the configured interval. When its
trimmed stdout is non-empty and differs from the previous successful poll,
SignalBox starts the named pipeline and passes stdout as the pipeline input.
Unchanged output is suppressed; the last output hash is stored under
`.signalbox/watchers/` so restarting the server does not replay it. A failed
command is logged and retried on the next interval. The default command timeout
is two minutes.

```yaml
watchers:
  - name: jira-queue
    command: jira
    args: [issues, list, --jql, "project = OPS AND status = 'To Do'", --output, json]
    interval: 5m
    timeout_seconds: 30
    pipeline: default
```

The command must be available on `PATH` (or use an absolute path). Keep its
output stable between polls unless a new pipeline run is intended.
