# Command line

[Back to SignalBox](../README.md)

## Command line

`signalbox run` runs a pipeline in the terminal. Execution commands manage
executions on a running `signalbox serve` (the address comes from
`signalbox.yaml`, or pass `--addr host:port`), so anything the web UI can do
can be scripted:

```text
signalbox list [--page N] [--page-size N]
signalbox show <execution> [--json]
signalbox start <pipeline> [--input text] [--param k=v]... [--file path]...
signalbox approve <execution> [--run id] [--comment text] [--by name]
signalbox reject <execution> [--run id] [--comment text] [--by name]
signalbox edit <execution> [--run id] (--output text | --file path) [--by name]
signalbox retry <execution> [--run id]
signalbox restart <execution> [--run id | --node id] [--stage id] [--force]
signalbox rerun <execution>
signalbox cancel <execution>
signalbox delete <execution>
signalbox chunks ...
signalbox registry ...
```

When exactly one run is waiting, `approve`, `reject`, `edit`, and `retry` pick
it automatically; otherwise they list the candidates and ask for `--run`.
`show` prints the run table, including reused runs and restart warnings.
`signalbox help executions` and `signalbox help registry` print the details.

`signalbox run` prompts for approvals on the terminal; pass `--yes` to
auto-approve every gate (useful in scripts/CI). Pass pipeline parameters with
a repeatable `--param name=value`. `signalbox serve --addr
127.0.0.1:8485` overrides the configured listen address; `signalbox version`
prints the build version. Tagged `go install` builds report their module version
automatically (including prereleases and commit pseudo-versions). Release
workflow stamps take precedence; unstamped local source builds report `dev`.

## Export a standalone Python runner

```powershell
signalbox export tech-article --format python --output article.py
python article.py --param "topic=Building reliable agent workflows" --output-dir article-run
python article.py --resume --output-dir article-run
```

`signalbox export` snapshots the pipeline and its resolved CLI runtime settings
into one Python 3.10+ script. Running it needs Python and the configured agent
CLIs, but not SignalBox, a server, PyYAML, or other Python packages. Authenticate
the CLIs normally on the destination machine; export does not read or embed
environment credentials or user-level CLI configuration. Prompts and configured
command arguments are included, so review an export before sharing it.
Existing output scripts are protected unless `--force` is supplied.
Use `signalbox help export` or `python article.py --help` for command options.
Alternatively open a pipeline in the web UI and select **Export Python** to
download the same standalone script. Export uses the saved YAML, not unsaved
editor changes. Unsupported features are reported in the page instead of
downloading an incomplete script.

The first version supports **one sequential handoff chain** of `standard`,
`llm_gate`, and `approval` nodes using `codex`, `claude`, `copilot`, or generic
`cli` runtimes. It preserves string/integer/number/boolean parameters,
additional inputs from earlier nodes, context selection, fixed model/effort/speed
settings, per-call timeouts, LLM verdict validation and bounded rework to earlier
nodes. Human approvals prompt in the terminal; `--yes` explicitly auto-approves
them. Human rejection uses the explicit earlier rejection target, the last
approved checkpoint, or immediate upstream work, as in SignalBox.
Presentation-only Markdown formatting is not needed in a terminal.

Parallel branches/joins, conditional or templated handoffs, rejection-only
branches, other node/runtime types, retries, schemas, MCP pass-through,
knowledge bases, uploaded files, and parameterized runtime settings are
rejected at export time, not silently omitted.

Agent commands run in the current directory unless `--workdir` is supplied.
Use the destination project directory for repository-aware prompts. Local
executables and paths in configured command arguments must exist there.
`--input` sets the initial payload; parameters use repeatable `--param name=value`
flags. Only visible Codex messages and selected activity events are logged;
private reasoning is excluded. Other CLI runtimes stream their text output.
Progress goes to stderr; the final handoff payload goes to stdout.

The output directory holds numbered per-attempt `.log` files, completed-step
`.txt` outputs, `result.txt`, and an atomically saved `checkpoint.json`.
`--resume` reuses saved parameters, input, and working directory and skips
completed steps; the unfinished step may run again, so consider side effects
before resuming. A lock prevents two runners from using the same output directory.
The checkpoint must match the exported snapshot. Rejected runs require a new
output directory rather than bypassing gate limits. These files can contain
private article/project material; protect them like execution logs.

Export does not prevent system sleep or provide SignalBox's detached-worker,
web UI, shared concurrency, or execution-history integration. Timeouts still
bound individual CLI calls.
