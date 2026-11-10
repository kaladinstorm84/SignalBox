# Structured responses and schemas

[Back to SignalBox](../README.md)

### Structured responses

Put JSON Schema files (`.json`, `.yaml` or `.yml`) in the project's
`schemas/` directory (`schemas_dir` in `signalbox.yaml`) and reference them by
file name without the extension. A standard, code or `llm_gate` node's
`output.schema` makes its answer a validated JSON document; a pipeline's
`input.schema` validates the execution input before anything runs.

The web UI's **Schemas** page lists these files, their definitions, validation
errors, and links to the pipelines and agents that use them.

```yaml
name: structured-triage
input:
  schema: ticket            # schemas/ticket.json; non-matching input is refused
entry: triage
agents:
  - id: triage
    runtime: codex
    prompt: Classify the ticket.
    output:
      schema: triage        # schemas/triage.json
      on_invalid: repair    # repair (default for agents) or fail (default for code)
      max_repairs: 2        # 0-10, default 2
    handoff:
      - to: on_call
        condition: output.severity == "high"
        input: "Escalation: {{ output.summary }} (first owner {{ output.actions.0.owner }})"
      - to: support_queue
        condition: output.severity != "high"
```

- **Validation.** Schemas default to JSON Schema 2020-12 (set `$schema` for
  another draft). Local `$ref`s such as `#/$defs/...` work; references to other
  files or URLs are refused. `signalbox validate` checks every schema file.
- **Extraction.** The answer may be bare JSON, a fenced ```` ```json ```` block,
  or prose around one JSON value; the first document found is validated. On
  success the run's output is replaced with the document as indented JSON.
- **Repair.** With `on_invalid: repair`, an invalid answer is sent back to the
  same node with its original input, the rejected answer, and the validation
  errors (each as `at /json/pointer: message`). After
  `max_repairs` invalid repairs the run fails with the errors.
  `on_invalid: fail` fails on the first invalid answer. Runtime retries still
  apply to each call; schema failures are not retried automatically. Code
  steps receive the repair text on stdin, so a script can react to it.
- **Runtimes.** CLI runtimes (`codex`, `claude`, `copilot`, `cli`) get the
  schema and a "reply with JSON only" instruction appended to the prompt.
  `openai` also sends it as `response_format` (`json_schema`, non-strict);
  SignalBox still validates the answer. The `mock` runtime returns a sample
  document generated from the schema, so demos route on real fields.
- **Field routing.** A handoff `condition` can compare an output field with
  `==` or `!=` against a JSON literal (`"high"`, `'high'`, `3`, `true`,
  `null`): `output.severity == "high"`, `output.actions.0.owner != null`.
  Array elements are addressed by index. A missing field compares as `null`.
  Matching routes behave like unconditional handoffs; if none match, the
  output becomes a result of the execution.
- **Templates.** Handoff `input` templates can use `{{ output.path }}`
  alongside `{{ input }}`, `{{ output }}` and `{{ comment }}`. Strings are
  inserted as-is; objects and arrays as compact JSON; missing fields as empty
  text. Field conditions and templates need `output.schema` on the node.
- **Gates.** On an `llm_gate`, the schema describes the verdict. It must
  still carry the string fields `decision` (`approve`/`reject`) and
  `feedback`, and may add others (scores, issue lists). `output.*` conditions
  and templates on a gate read the verdict, and field-conditioned routes run
  only on approval. `{{ output }}` remains the input the gate passes on.

Each run records the schema and every validation attempt (errors and, for
invalid attempts, the rejected answer). The web UI shows a schema badge on
the run, the checks with their errors, and gate verdicts; the Pipelines page
shows each node's contract and lists the project's schema files with the
pipelines that use them. Registry packages bundle the schema files their
pipeline uses and install them into the consumer's `schemas_dir`.

Demos: `structured-triage` (input schema, field routing, templates and a
schema-validated gate verdict; start it with
`{"customer": "Acme", "summary": "Invoice total does not match the order"}`)
and `structured-repair` (a script whose invalid answer is repaired from the
validation errors).
