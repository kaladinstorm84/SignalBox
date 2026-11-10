# Development

[Back to SignalBox](../README.md)

## Development

```bash
go build ./...
go test ./...
```

Layout:

- `cmd/signalbox` — CLI entry point
- `internal/cli` — commands (`init`, `validate`, `serve`, `run`, `chunks`, execution commands, `registry`) and scaffolding templates
- `internal/config` — `signalbox.yaml` loading
- `internal/pipeline` — pipeline YAML schema and validation
- `internal/engine` — execution engine (handoffs, orchestrator stages, approvals, recovery/restart) and persistence
- `internal/registry` — pipeline packages: publish, verify, install, lock file
- `internal/runtime` — runtime adapters (OpenAI API, agent CLIs, mock) and local script execution
- `internal/server` — HTTP API and embedded web UI
- `internal/watcher` — command polling and persisted output-change detection
