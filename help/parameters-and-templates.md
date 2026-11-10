# Parameters, imports and templates

[Back to SignalBox](../README.md)

### Parameters

A pipeline can declare `params` and reference them as `${{ params.name }}`.
Values are chosen when a run starts (the **New run** form, `signalbox run
--param name=value`, or `"params"` in `POST /api/executions`), so one pipeline
can serve several variants.

```yaml
params:
  topic:
    required: true
    description: The subject of the briefing.
  audience:
    values: [engineers, executives]   # enum; rendered as a dropdown
    default: engineers
  depth: {type: integer, default: "3"}
  writer_model: ""                    # shorthand: string with this default
```

- Types are `string` (default), `integer`, `number`, and `boolean`. `values`
  restricts the value to a list. A param is either `required` or has a
  `default`, not both. A param with no default and not required is empty.
- References are substituted into the pipeline description, agent
  `description`, `prompt`, and `model`, `handoff` `input` templates, stage
  prompts, and chunk `initial` titles. Unknown references are validation
  errors; nothing else (IDs, runtimes, routes) can be parameterised.
- Values are validated before the run starts; unknown names, missing required
  values, and wrong types are rejected. Values are substituted as plain text.
- The resolved values are saved with the execution and shown on its page.
  **Rerun** and **Retry from here** reuse them. They are stored in plain
  text, so do not pass secrets as params.
- Watchers start runs with default values, so a watcher cannot target a
  pipeline with a required param that has no default.

Demo: `briefing-params`.

### Imports and templates

Agents can be shared between pipelines. `templates` are partial agent
definitions; an agent (or another template) inherits from them with
`extends` (a name or a list, applied in order). `imports` pull templates and
agents from another YAML file.

```yaml
imports:
  - from: ../shared/review-kit.yaml   # relative to this file
    templates: [gate_reviewer]        # omit to import all templates
    agents: [changelog_writer]        # omit to import all agents
templates:
  - id: strict_reviewer
    extends: gate_reviewer
    retry: {count: 2}
agents:
  - id: security_review
    extends: strict_reviewer
    prompt: Review for security problems only.
    reject-to: implementer
```

- Merging: maps (such as `retry` and `context`) deep-merge, and scalars and
  lists in the child replace the parent's. A child that sets
  `context: {strategy: none}` still inherits the parent's `window`, which
  fails validation, so set `window: 0` (and `max_chars: 0`, if inherited)
  explicitly when overriding.
- Imported files hold `templates` and/or `agents` and may import others.
  Shared files should live outside `pipelines_dir` (for example `shared/`), so
  they are not loaded as pipelines. Local agents come first, followed by
  imported agents in import order.
- Errors are reported for import or `extends` cycles (with the chain), more
  than 10 levels of nesting, duplicate agent or template IDs across files,
  unknown templates or selected agents, and non-YAML files.
- Imports must stay inside the project directory (symlinks are resolved)
  unless `allow_external_imports: true` is set in `signalbox.yaml`.
- The **Pipelines** page lists each pipeline's imported files. A change to a
  shared file is picked up the next time pipelines reload.

Demo: `review-kit` with `shared/review-kit.yaml`.
