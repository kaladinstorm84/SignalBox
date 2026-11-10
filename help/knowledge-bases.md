# Knowledge bases

[Back to SignalBox](../README.md)

### Knowledge bases

Create and manage project knowledge bases from **Knowledge bases** in the web
UI. Add text documents in the editor or upload plain-text `.txt`, `.md`, `.csv`,
`.tsv`, `.json`, `.yaml`, `.yml`, `.xml`, and `.log` files (up to 1 MiB each).
Knowledge bases are stored in the project's `knowledge-bases/` directory.

Attach one or more bases to a standard agent, `llm_gate`, orchestrator, or
session with `knowledge_bases`. The complete text of each attached base is
appended to that agent's runtime input on every call; this version does not
perform semantic retrieval. Orchestrator agents use the field for their
synthesis calls, and session participants can be assigned bases individually.

```yaml
- id: researcher
  runtime: codex
  knowledge_bases: [product-handbook, support-policies]
  prompt: Answer using the supplied references; call out missing information.
```

Documents are read when an agent runs, so updates apply to subsequent calls.
The combined attached knowledge for one invocation is limited to 5 MiB.

- **Runnable demo:** [knowledge-base-support](../testproject/pipelines/knowledge-base-support.yaml)
  attaches the sample [product handbook](../testproject/knowledge-bases/demo-product-handbook/product.md)
  and [support policies](../testproject/knowledge-bases/demo-support-policies/support.md)
  to one support agent. It uses the project's existing `codex` runtime and
  normally takes one LLM call, without an approval gate. From `testproject`,
  run:

  ```powershell
  go run ..\cmd\signalbox run knowledge-base-support --input "Can a team on Starter export its data, and how long is an export available?"
  ```

  The response should explain that Starter includes CSV exports and that
  download links expire after 7 days, citing both documents. For a no-cost
  wiring check, change the pipeline's `assistant` template runtime to `mock`;
  mock echoes the input and attached references rather than answering.
