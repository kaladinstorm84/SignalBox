# Environment Variables Example Workflow

A complete example workflow is now available in the testproject demonstrating the three-level environment variable feature.

## Quick Start

### Run the Example

```bash
cd testproject
../signalbox run variables-demo --input "test"
```

Or use the web UI:

```bash
../signalbox serve
```

Then navigate to the web UI and run the `variables-demo` pipeline.

## What the Example Shows

The `variables-demo` pipeline demonstrates:

1. **Project-level variables** defined in `signalbox.yaml`:
   ```yaml
   variables:
     PROJECT_VAR: "from-project"
     SHARED_VAR: "overridden-by-pipeline"
   ```

2. **Pipeline-level variables** defined in `pipelines/variables-demo.yaml`:
   ```yaml
   variables:
     LOG_LEVEL: "debug"
     ENVIRONMENT: "staging"
   ```

3. **Step-level variables** defined in the agent configuration:
   ```yaml
   agents:
     - id: check-variables
       variables:
         STEP_VAR: "from-step"
         SHARED_VAR: "overridden-by-step"
   ```

## Precedence Demonstration

The example shows how variables cascade with proper precedence:

```
Project Level (signalbox.yaml)
         ↓
Pipeline Level (pipelines/variables-demo.yaml) - overrides project
         ↓
Step Level (agent variables) - overrides both
```

### Variable Results at Execution

For variables defined at multiple levels, the step-level value takes precedence:

| Variable | Project | Pipeline | Step | Result |
|----------|---------|----------|------|--------|
| PROJECT_VAR | from-project | — | — | `from-project` |
| LOG_LEVEL | info | debug | — | `debug` |
| ENVIRONMENT | — | staging | — | `staging` |
| STEP_VAR | — | — | from-step | `from-step` |
| SHARED_VAR | overridden-by-pipeline | — | overridden-by-step | `overridden-by-step` |

The last column shows which value is actually used when the pipeline executes.

## Workflow Description

The `variables-demo` pipeline has two agents:

### Agent 1: `check-variables`
- **Type**: Standard agent using mock runtime
- **Purpose**: Demonstrate that all variables are available
- **Variables Defined**:
  - `STEP_VAR: "from-step"` (step level)
  - `SHARED_VAR: "overridden-by-step"` (overrides pipeline level)
- **Action**: Explains which variables were available and their sources

### Agent 2: `show-cascade`
- **Type**: Standard agent
- **Purpose**: Explain how the precedence worked
- **Action**: Summarizes the cascade mechanism

## How to Use Variables in Your Own Pipelines

### 1. Define Project Variables

In `signalbox.yaml`:
```yaml
variables:
  API_BASE_URL: "https://api.example.com"
  LOG_LEVEL: "info"
  TIMEOUT: "30"
```

### 2. Define Pipeline Variables

In `pipelines/my-pipeline.yaml`:
```yaml
name: my-pipeline
variables:
  LOG_LEVEL: "debug"      # Overrides project
  API_KEY: "secret-123"   # New variable
```

### 3. Define Step Variables

In agents within the pipeline:
```yaml
agents:
  - id: my-agent
    type: standard
    prompt: "Process data"
    variables:
      LOG_LEVEL: "trace"   # Overrides both project and pipeline
      BATCH_SIZE: "100"    # Step-specific variable
```

### 4. Access Variables in Code Nodes

In Python code nodes:
```python
import os

api_url = os.environ.get('API_BASE_URL')
log_level = os.environ.get('LOG_LEVEL')
timeout = int(os.environ.get('TIMEOUT', '30'))

print(f"API: {api_url}")
print(f"Level: {log_level}")
print(f"Timeout: {timeout}s")
```

## Common Use Cases

### 1. Environment-Specific Configuration

```yaml
# signalbox.yaml
variables:
  ENVIRONMENT: "production"
  API_BASE_URL: "https://api.prod.example.com"

# pipelines/analytics.yaml (staging override)
variables:
  ENVIRONMENT: "staging"
  API_BASE_URL: "https://api.staging.example.com"
```

### 2. Debug Levels

```yaml
# Project: default to info
variables:
  LOG_LEVEL: "info"

# Pipeline: debugging requires more detail
variables:
  LOG_LEVEL: "debug"

# Step: specific agent needs verbose output
variables:
  LOG_LEVEL: "trace"
```

### 3. Feature Flags

```yaml
variables:
  ENABLE_CACHING: "true"
  ENABLE_OPTIMIZATION: "false"
  NEW_ALGORITHM: "false"
```

### 4. Performance Tuning

```yaml
variables:
  BATCH_SIZE: "100"
  WORKER_THREADS: "4"
  MEMORY_LIMIT: "1g"
  TIMEOUT_SECONDS: "300"
```

## Testing the Example

After running the example, you can verify it worked by checking:

1. **In the web UI**: Go to Executions and view the results
2. **In the terminal**: Check the output from the agents
3. **Variable values**: The agents will show which variables were received

## Files Involved

- `testproject/signalbox.yaml` - Project-level configuration with variables
- `testproject/pipelines/variables-demo.yaml` - The example pipeline
- `help/variables.md` - Complete user documentation

## See Also

- [VARIABLES_IMPLEMENTATION.md](VARIABLES_IMPLEMENTATION.md) - Technical details
- [help/variables.md](help/variables.md) - Full user documentation
- [VARIABLES_COMPLETE.md](VARIABLES_COMPLETE.md) - Implementation summary
