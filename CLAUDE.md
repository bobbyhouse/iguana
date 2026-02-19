# iguana — CLAUDE.md

## BAML (Go)

BAML defines typed LLM calls. The source lives in `baml_src/`; the generated
Go client lives in `baml_client/` (never edit that directory manually).

**After every `.baml` edit, run:**
```
baml-cli generate
```
This regenerates `baml_client/` and auto-runs `gofmt` + `goimports` on the
output (configured in `baml_src/generators.baml`).

### File layout

| File | Purpose |
|------|---------|
| `baml_src/generators.baml` | Output type (`go`), output dir, package name |
| `baml_src/clients.baml` | LLM client definitions (Anthropic, OpenAI, …) |
| `baml_src/system_model.baml` | Classes + functions for system model inference |
| `baml_client/types/classes.go` | Generated Go structs (one per BAML class) |
| `baml_client/*.go` | Generated function wrappers (one per BAML function) |

### BAML syntax

**Classes** — map to Go structs with JSON tags:
```
class Foo {
  name string        // Go field: Name string `json:"name"`
  items string[]     // Go field: Items []string `json:"items"`
  opt string?        // Go field: Opt *string `json:"opt"`
  nested Bar         // Go field: Nested Bar `json:"nested"`
  @description("hint shown in output_format")
  score float        // adds description to LLM output format
}
```

Field naming: BAML `snake_case` → Go field `Snake_case` (underscore preserved),
JSON tag `"snake_case"`. Example: `primary_mutators` → `Primary_mutators`.

**Functions** — one LLM call per function:
```
function InferFoo(input: Foo[]) -> BarResult {
  client "ClientName"
  prompt #"
    System instructions here.

    Input: {{ input }}

    {{ ctx.output_format }}
  "#
}
```

**Always include `{{ ctx.output_format }}`** in every prompt — it injects the
structured output instructions the LLM needs to produce valid JSON.

### Adding a field to a class

1. Add the field in the `.baml` class definition
2. Run `baml-cli generate`
3. The field appears in `baml_client/types/classes.go`
4. Reference it in Go as `types.ClassName.Field_name`

### Using the generated client in Go

```go
import (
    b     "iguana/baml_client"
    "iguana/baml_client/types"
)

result, err := b.InferSystemModel(ctx, summaries) // summaries: []types.PackageSummary
if err != nil { ... }
for _, domain := range result.State_domains { // result: *types.SystemModelInference
    _ = domain.Id
}
```

### Clients (`baml_src/clients.baml`)

```
client<llm> MyClient {
  provider anthropic          // reads ANTHROPIC_API_KEY
  options {
    model "claude-sonnet-4-20250514"
  }
}
```

Supported providers: `anthropic`, `openai`, `openai-responses`, `google-ai`,
`vertex-ai`, `aws-bedrock`, `openai-generic` (for Ollama etc.), `round-robin`,
`fallback`.

### Attributes

- `@description("text")` — added to the output format shown to the LLM
- `@alias("json_key")` — overrides the JSON key used in LLM output parsing
- `@skip` — field is excluded from serialization entirely

### Known issues

The `on_generate` command in `generators.baml` prefixes `GOEXPERIMENT=` to clear
a shell env var (`GOEXPERIMENT=aliastypeparams`) that Go 1.26 does not recognize.
This is required for `gofmt` and `goimports` to run cleanly. Do not remove this
prefix.

### Invariants

- Never manually edit `baml_client/` — always regenerate with `baml-cli generate`
- Every prompt must include `{{ ctx.output_format }}`
- The Go package is `iguana` (set in `generators.baml` → `client_package_name`)
