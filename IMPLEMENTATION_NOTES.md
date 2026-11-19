# Implementation Summary: Environment Variables Support

## Overview

Added support for environment variables at three levels of precedence in SignalBox:
- **Project-level** (signalbox.yaml): `variables` property
- **Pipeline-level** (pipeline YAML): `variables` property  
- **Step-level** (agent/node config): `variables` property

Variables cascade with higher levels overriding lower ones: Project → Pipeline → Step

## Changes Made

### 1. Configuration Structures

#### [config/config.go](internal/config/config.go)
- Added `Variables map[string]string` field to `Config` struct
- Allows project-wide environment variable definitions in signalbox.yaml

#### [pipeline/pipeline.go](internal/pipeline/pipeline.go)
- Added `Variables map[string]string` field to `Pipeline` struct
- Added `Variables map[string]string` field to `Node` struct
- Pipeline-level variables apply to all agents in that pipeline
- Step-level variables apply to specific agents/nodes only

#### [runtime/runtime.go](internal/runtime/runtime.go)
- Added `Variables map[string]string` field to `Request` struct
- Carries merged variables to runtime invocation

### 2. Engine Implementation

#### [engine/engine.go](internal/engine/engine.go)
- Added `config *config.Config` field to `Engine` struct
- Added import for `config` package
- Added `NewWithConfig()` function to create engine with config access
- Added `mergeVariables()` method to merge variables with proper precedence:
  ```go
  func (eng *Engine) mergeVariables(node *pipeline.Node, p *pipeline.Pipeline) map[string]string
  ```
- Updated all agent invocation points to call `mergeVariables()` and include variables in requests:
  - Standard agents in `runNode()`
  - LLM gates in `runLLMGate()`
  - Orchestrator prompts in `invokeOrchPrompt()`
  - Code nodes and OpenSpec nodes

#### [engine/integrations.go](internal/engine/integrations.go)
- Updated `agentRequest()` function to initialize Variables field

#### [engine/session.go](internal/engine/session.go)
- Updated `sessionTurn()` to include merged variables for session participants

#### [cli/cli.go](internal/cli/cli.go)
- Updated `buildEngine()` to use `NewWithConfig()` instead of `NewWithLimit()`
- Passes config to engine for variable support

### 3. Runtime Execution

#### [runtime/cli.go](internal/runtime/cli.go)
- Updated `invoke()` function to merge request variables into environment:
  ```go
  for key, value := range req.Variables {
      env = append(env, key+"="+value)
  }
  ```
- Variables are passed to CLI runtimes (codex, claude, copilot, custom commands)

## Precedence Order

Variables are merged with the following priority (lowest to highest):

1. **Project** (`signalbox.yaml` → `variables`)
2. **Pipeline** (pipeline YAML → `variables`)  
3. **Step** (agent definition → `variables`)

Example precedence resolution:
- Project defines: `LOG_LEVEL: "info"`, `API_URL: "https://api.prod.com"`
- Pipeline overrides: `LOG_LEVEL: "debug"`
- Step overrides: `LOG_LEVEL: "trace"`

Result: `LOG_LEVEL: "trace"`, `API_URL: "https://api.prod.com"`

## Usage Examples

### Project Level (signalbox.yaml)
```yaml
version: 1
project: myproject

variables:
  API_KEY_ENV: "MY_API_KEY"
  LOG_LEVEL: "info"
  TIMEOUT: "30"
```

### Pipeline Level (pipeline YAML)
```yaml
name: data-pipeline
entry: processor

variables:
  LOG_LEVEL: "debug"
  BATCH_SIZE: "1000"
  API_URL: "https://staging-api.example.com"
```

### Step Level (agent in pipeline)
```yaml
agents:
  - id: processor
    type: code
    language: python
    code-source: |
      import os
      print(f"API_URL={os.environ.get('API_URL')}")
    variables:
      API_URL: "https://custom-api.example.com"
      DEBUG_MODE: "true"
```

## Testing

All existing tests pass:
- `./internal/config` tests: Configuration parsing and validation
- `./internal/pipeline` tests: Pipeline structure and validation
- `./internal/engine` tests: Execution engine and agent invocation
- `./internal/runtime` tests: Runtime invocation and CLI execution

The feature integrates seamlessly with existing functionality:
- Code nodes receive variables in environment
- Agent CLIs receive variables in environment
- Sessions and orchestrators receive merged variables
- LLM gates and approvals receive variables

## Documentation

Created comprehensive documentation in [help/variables.md](help/variables.md):
- Precedence explanation
- Configuration examples at all levels
- Usage in code nodes (Python, etc.)
- Use cases (API credentials, debug levels, feature flags, performance tuning)
- Limitations and best practices

## Backward Compatibility

✅ Fully backward compatible:
- Variables fields are optional in all structures
- Existing pipelines and projects work without modification
- Empty/nil variable maps are handled gracefully
- No breaking changes to APIs or configuration formats

## Files Modified

1. `internal/config/config.go` - Added Variables field to Config
2. `internal/pipeline/pipeline.go` - Added Variables to Pipeline and Node
3. `internal/runtime/runtime.go` - Added Variables to Request
4. `internal/runtime/cli.go` - Merge variables into environment
5. `internal/engine/engine.go` - Core merge logic and invocation points
6. `internal/engine/integrations.go` - Updated agentRequest
7. `internal/engine/session.go` - Session variable support
8. `internal/cli/cli.go` - Use NewWithConfig
9. `testproject/signalbox.yaml` - Example project variables
10. `help/variables.md` - Complete documentation

## Implementation Quality

- ✅ All tests pass
- ✅ Follows existing code patterns and conventions
- ✅ Proper error handling (nil checks for config)
- ✅ Clean variable precedence logic
- ✅ Comprehensive documentation
- ✅ Backward compatible
- ✅ No external dependencies added
