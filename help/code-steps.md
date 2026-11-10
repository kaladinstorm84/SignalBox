# Code steps

[Back to SignalBox](../README.md)

### Code Steps

```yaml
- id: Verify
  type: code
  language: python
  code-source: libs/verify.py
  timeout_seconds: 30
  codes:
    success: 0
    fail: 1
    retry: 2
  handoff: [security_judge]
```

Connect an upstream node with `handoff: [Verify]`, or list the code node in an
orchestrator stage's `agents`. `code-source` is relative to the project directory
containing `signalbox.yaml`; scripts also run with that working directory.
Supported languages are `python`, `javascript` (Node.js), and `powershell`;
the corresponding `python`, `node`, or `powershell` command must be on `PATH`.
Scripts run locally with the server user's permissions, not in a sandbox.

The full assembled handoff input is sent as UTF-8 text on **stdin**, including
any `additional-input` sections. Write the result to **stdout** and diagnostics
to **stderr**. With `codes`, the mapped `success` hands stdout downstream and
`fail` immediately fails the execution. The mapped `retry` acts like rejection:
it restarts work after the last upstream approved gate in the same stage/scope,
or retries the immediate upstream agents if no earlier approval exists. Both
stdout and stderr become rejection feedback appended to the rework input.
The rejected code run remains in the timeline and does not hand off a result.
Without upstream work to retry, rejection ends the execution.

All three mappings are required when `codes` is present and must have distinct,
non-negative values. Without `codes`, nonzero exits retain the manual-retry
behavior. Unmapped exits and process errors (including timeouts) also pause
for manual retry. The script is reloaded on each call. Code steps share the
runtime concurrency cap and cancellation handling; the default timeout is
600 seconds.

For structured input, an upstream node can return JSON and the script can parse
it directly (without additional labeled handoff sections):

```python
import json
import sys

data = json.load(sys.stdin)
if not data.get("tests"):
    print("Validation failed: no tests supplied", file=sys.stderr)
    sys.exit(1)
print(json.dumps({"validated": True, "tests": data["tests"]}))
```

The QA example's `libs/verify.py` currently checks for non-empty planner output
and passes it through; replace its body with your validation rules.
