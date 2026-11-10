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
