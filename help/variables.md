# Environment Variables

SignalBox supports setting environment variables at multiple levels with a clear precedence order. This allows you to configure common variables at the project level, override them at the pipeline level, and have fine-grained control at the step level.

## Precedence Order

Environment variables are merged with the following precedence (lowest to highest):

1. **Project-level** (signalbox.yaml): `variables` property
2. **Pipeline-level** (pipeline YAML): `variables` property  
3. **Step-level** (pipeline YAML, per agent): `variables` property

Higher precedence levels override lower ones. A variable defined at the step level will override the same variable defined at the project or pipeline level.

## Project-Level Variables

Define environment variables in your project's `signalbox.yaml` file that apply to all executions:

```yaml
version: 1
project: myproject

# Project-level variables (lowest precedence)
variables:
  API_BASE_URL: "https://api.example.com"
  LOG_LEVEL: "info"
  TIMEOUT: "30"
```

These variables are available to all pipelines and agents in the project.

## Pipeline-Level Variables

Define variables in a specific pipeline YAML file that override project variables for that pipeline only:

```yaml
name: data-processing
description: Process data with custom settings
entry: processor

# Pipeline-level variables (medium precedence)
variables:
  LOG_LEVEL: "debug"      # Overrides project LOG_LEVEL
  BATCH_SIZE: "1000"      # New variable specific to this pipeline
  API_BASE_URL: "https://staging-api.example.com"  # Overrides project URL

agents:
  - id: processor
    type: standard
    runtime: codex
    prompt: "Process the data"
```

## Step-Level Variables

Define variables on specific agents/steps that override both project and pipeline variables:

```yaml
agents:
  - id: processor
    type: standard
    runtime: codex
    prompt: "Process the data"
    
    # Step-level variables (highest precedence)
    variables:
      LOG_LEVEL: "trace"       # Overrides project and pipeline LOG_LEVEL
      DEBUG_MODE: "true"       # New variable specific to this step
      TIMEOUT: "60"            # Overrides project TIMEOUT
  
  - id: validator
    type: code
    language: python
    code-source: |
      import os
      print(f"API_BASE_URL: {os.environ.get('API_BASE_URL')}")
      print(f"LOG_LEVEL: {os.environ.get('LOG_LEVEL')}")
    
    # Different variables for this step
    variables:
      LOG_LEVEL: "warning"     # Different from processor
      STRICT_MODE: "true"      # Specific to validator
```

## Usage in Code Nodes

Code nodes receive environment variables through the runtime's environment:

### Bash/Shell Example

```yaml
- id: check-config
  type: code
  language: bash
  code-source: |
    echo "Configuration:"
    echo "API: $API_BASE_URL"
    echo "Log Level: $LOG_LEVEL"
    echo "Batch Size: $BATCH_SIZE"
  variables:
    API_BASE_URL: "https://api.example.com"
    LOG_LEVEL: "debug"
    BATCH_SIZE: "1000"
```

### Python Example

```yaml
- id: process-data
  type: code
  language: python
  code-source: |
    import os
    api_url = os.environ.get('API_BASE_URL', 'https://api.example.com')
    log_level = os.environ.get('LOG_LEVEL', 'info')
    batch_size = int(os.environ.get('BATCH_SIZE', '100'))
    print(f"Processing {batch_size} items at {log_level} level")
  variables:
    API_BASE_URL: "https://api.example.com"
    LOG_LEVEL: "info"
    BATCH_SIZE: "500"
```

## Usage with Agent CLIs

Environment variables are passed to agent CLIs (codex, claude, copilot, etc.) and are available during execution:

```yaml
agents:
  - id: analyzer
    type: standard
    runtime: codex
    prompt: "Analyze the data"
    variables:
      ANALYSIS_MODE: "detailed"
      REPORT_FORMAT: "json"
```

The agent CLI receives these variables in its environment and can reference them if needed.

## Example: Multi-Level Configuration

Here's a complete example showing all three levels working together:

**signalbox.yaml:**
```yaml
version: 1
project: analytics

variables:
  API_KEY_ENV: "ANALYTICS_API_KEY"  # Points to env var containing actual key
  REGION: "us-west-2"
  ENVIRONMENT: "production"
  LOG_LEVEL: "info"
```

**pipeline: production-analytics.yaml:**
```yaml
name: production-analytics
entry: gather

variables:
  ENVIRONMENT: "production"  # Already set in project, explicit here
  LOG_LEVEL: "warning"       # Override project info to warning
  SAMPLE_RATE: "0.1"         # New pipeline-specific variable

agents:
  - id: gather
    type: standard
    runtime: codex
    prompt: "Gather production analytics"
    variables:
      LOG_LEVEL: "error"     # Most specific: errors only for this step
      SAMPLE_RATE: "0.01"    # More aggressive sampling for this step
      TIMING_ENABLED: "true" # Step-specific variable
    handoff: [analyze]

  - id: analyze
    type: standard
    runtime: codex
    prompt: "Analyze the gathered data"
    # Uses pipeline and project variables (LOG_LEVEL: "warning")
```

## Common Use Cases

### 1. API Credentials and Endpoints

```yaml
variables:
  API_BASE_URL: "https://api.prod.example.com"
  API_TIMEOUT: "30"
  API_RETRY_COUNT: "3"
```

### 2. Debug/Trace Levels

```yaml
# Project
variables:
  LOG_LEVEL: "info"

# Pipeline - staging environment
variables:
  LOG_LEVEL: "debug"

# Step - verbose debugging
variables:
  LOG_LEVEL: "trace"
```

### 3. Feature Flags

```yaml
variables:
  ENABLE_CACHING: "true"
  ENABLE_PARALLEL_PROCESSING: "false"
  NEW_ALGORITHM_ENABLED: "true"
```

### 4. Performance Tuning

```yaml
variables:
  BATCH_SIZE: "100"
  WORKER_THREADS: "4"
  MEMORY_LIMIT: "1g"
  TIMEOUT_SECONDS: "300"
```

## Environment Variable Limitations

- Variable names must be valid environment variable names (alphanumeric and underscore)
- Special characters in values are supported, but should be quoted in YAML if necessary
- Variables are passed to child processes as strings
- When used in code nodes, convert to appropriate types (integer, boolean, etc.) as needed

## See Also

- [Configuration](configuration.md) - Project-wide configuration
- [Pipelines](pipelines.md) - Pipeline definitions and structure
- [Code Steps](code-steps.md) - Using code nodes with environment variables
