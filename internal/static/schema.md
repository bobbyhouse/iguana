# iguana static plugin — evidence bundle schema

Each evidence bundle is a markdown file with YAML frontmatter. The frontmatter
fields are defined below.

## Top-level fields

| Field    | Type   | Description |
|----------|--------|-------------|
| `plugin` | string | Always `"static"` |
| `schema` | string | Filename of this schema document (always `"schema.md"`) |
| `hash`   | string | SHA-256 hex digest of the source file at analysis time |
| `file`   | object | File identity (see below) |
| `package`| object | Package metadata (see below) |
| `functions` | array | Top-level function and method declarations |
| `types`  | array  | Top-level type declarations |
| `signals`| object | Boolean behavioral heuristics |

## `file` object

| Field | Type   | Description |
|-------|--------|-------------|
| `path`| string | Slash-separated path relative to the repository root |
| `ref` | string | Canonical git source URL: `git://<host>/<org>/<repo>@<commit>/<path>` |

## `package` object

| Field     | Type  | Description |
|-----------|-------|-------------|
| `name`    | string| Go package name |
| `imports` | array | Sorted list of `{path, alias?}` import objects |

## `functions` array elements

| Field      | Type    | Description |
|------------|---------|-------------|
| `name`     | string  | Function or method name |
| `exported` | bool    | Whether the identifier is exported |
| `receiver` | string  | Receiver type (methods only; omitted for functions) |
| `params`   | array   | Parameter type strings |
| `returns`  | array   | Return type strings |
| `ref`      | string  | Git source URL with line anchor: `...@<commit>/<path>#L<line>` |

## `types` array elements

| Field      | Type    | Description |
|------------|---------|-------------|
| `name`     | string  | Type name |
| `kind`     | string  | `"struct"`, `"interface"`, or `"alias"` |
| `exported` | bool    | Whether the identifier is exported |
| `fields`   | array   | Exported struct fields in declaration order (structs only) |

## `signals` object

All fields are boolean. Derived purely from static analysis (imports + AST).

| Field         | Meaning |
|---------------|---------|
| `fs_reads`    | Calls known file-read functions (os.Open, os.ReadFile, …) |
| `fs_writes`   | Calls known file-write functions (os.Create, os.WriteFile, …) |
| `db_calls`    | Imports database/sql or calls Query/Exec/Scan targets |
| `net_calls`   | Imports net or net/http |
| `concurrency` | Imports sync, uses goroutines, or references channel types |
| `yaml_io`     | Imports a yaml library or calls yaml.* |
| `json_io`     | Imports encoding/json or calls json.* |

## `ref` URL format

```
git://<host>/<org>/<repo>@<commit>/<path/to/file.go>
git://<host>/<org>/<repo>@<commit>/<path/to/file.go>#L<line>
```

The commit is the full 40-character SHA-1 returned by `git rev-parse HEAD` at
analysis time. Line numbers in `ref` fields are 1-based.

## Staleness detection

The `hash` field (SHA-256 of the source file) is compared on subsequent runs. If
the hash matches an existing bundle, the bundle is considered up to date and is
not regenerated.
