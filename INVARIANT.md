# iguana — Invariants

These invariants must hold before and after every change to the implementation.

## Integrity Invariants

1. **SHA256 correctness**: The `file.sha256` field must always equal
   `hex(sha256(os.ReadFile(file.path)))` at the moment of bundle creation.

2. **Staleness detection**: `validateEvidenceBundle` must return an error
   whenever the current file hash differs from `bundle.File.SHA256`.

3. **Version constant**: `EvidenceBundle.Version` is always `2`.

## Determinism Invariants

4. **Idempotency**: Running `createEvidenceBundle(filePath)` twice on the
   same unmodified file must produce byte-for-byte identical YAML output.

5. **No position data**: The YAML output must never contain line numbers,
   column numbers, or file offsets (e.g., no `line:`, `column:`, `offset:` keys).

6. **No timestamps**: The output must not contain timestamps, UUIDs, or
   environment-dependent values.

## Ordering Invariants

7. **Imports sorted**: `package.imports` is sorted lexicographically by `path`.

8. **Functions sorted**: `symbols.functions` is sorted by `name`.

9. **Types sorted**: `symbols.types` is sorted by `name`.

10. **Variables sorted**: `symbols.variables` is sorted by `name`.

11. **Constants sorted**: `symbols.constants` is sorted by `name`.

12. **Calls sorted**: `calls` is sorted by `from`, then by `to` for equal `from`.

## Path Invariants

13. **Forward slashes**: `file.path` uses `/` separators (via `filepath.ToSlash`).

14. **Output location**: The companion file is always written to
    `<input-path>.evidence.yaml` in the same directory as the input.

## Completeness Invariants

15. **All top-level functions captured**: Every `ast.FuncDecl` in the file
    appears exactly once in `symbols.functions`.

16. **All top-level type declarations captured**: Every `token.TYPE` spec
    appears in `symbols.types`.

17. **All top-level vars/consts captured**: Every `token.VAR`/`token.CONST`
    spec appears in `symbols.variables`/`symbols.constants`.

## Signal Invariants

18. **Signals are purely static**: Signals are derived only from imports, AST
    nodes, and call targets — never from runtime state.

19. **Signal monotonicity**: Adding more code to a file can only turn signals
    from `false` to `true`, never from `true` to `false`.

## Implementation Separation Invariants

20. **Generation is pure**: `createEvidenceBundle` does not write any files.

21. **Serialization is isolated**: `writeEvidenceBundle` only marshals and
    writes — it does not re-analyze the source file.

22. **Validation is read-only**: `validateEvidenceBundle` only reads the
    source file to recompute the hash — it does not modify anything.

## Directory Walk Invariants

23. **Relative paths in directory mode**: When `walkAndGenerate(root)` is used,
    `file.path` is relative to the provided root, using forward slashes.

24. **Skipped directories**: `vendor/`, `testdata/`, `examples/`, `docs/`, and
    directories whose name starts with `.` are skipped entirely during directory
    walking. Test files (`*_test.go`) are also skipped. Settings deny rules
    (INV-39) may skip additional paths.

25. **Deterministic walk order**: Directories and files within each directory
    are processed in sorted (lexicographic) order.

26. **One package load per directory**: `loadPackageForDir` is called once per
    unique directory, not once per `.go` file.

## System Model Invariants

27. **system_model.yaml is derived**: `system_model.yaml` is always generated
    from evidence bundles via `GenerateSystemModel`; it must never be manually
    edited. It is a derived artifact.

28. **System model arrays are sorted**: All arrays in the system model output
    are sorted alphabetically by `id` or primary key (filename, package name,
    or question text).

29. **Inferred elements have evidence_refs**: Every inferred element
    (`state_domains`, `trust_zones`) must have at least one entry in its
    `evidence_refs` list, tracing back to the bundles that justified it.

30. **Evidence ref format**: Evidence refs follow exactly:
    `bundle:<path>[#symbol:<name>|#signal:<name>]`
    — no other formats are valid.

31. **bundle_set_sha256 derivation**: `inputs.bundle_set_sha256` is a SHA256
    hash derived from all loaded bundle paths and hashes, sorted and joined by
    newline. It changes whenever any bundle is added, removed, or modified.

## CLI Dispatch Invariants

32. **Known subcommand dispatch**: When `os.Args[1]` exactly matches a registered
    command name, the command's `run` function is called with the remaining args
    (`os.Args[2:]`). No other handler is tried.

33. **Help flags**: `iguana`, `iguana --help`, and `iguana -h` all produce the
    same overall usage listing. `iguana help <cmd>` prints the long description
    for that command.

34. **Unknown subcommand error**: When `os.Args[1]` is not a known command name
    AND does not exist as a file/directory on disk, the process exits with a
    non-zero status and a message suggesting `iguana help`.

35. **Backward compat — file/dir mode**: When `os.Args[1]` is not a known
    subcommand name but names an existing file or directory, the existing
    file/directory behavior is preserved (no behavior change).

36. **Per-command usage on bad args**: When a subcommand receives wrong arguments,
    it prints its own `usage` line and exits non-zero. It does not panic.

37. **No-args is not an error path for help**: `iguana` with no args prints the
    help listing to stdout and exits 0 (not an error).

38. **Commands slice is the single source of truth**: All registered commands are
    in the `commands` slice. The dispatch loop, help listing, and `help <cmd>`
    all derive from the same slice — never hardcoded names.

## Settings Invariants

39. **Settings file location**: The settings file is always read from
    `.iguana/settings.yaml` relative to the analysis root. Absence of the file
    is not an error — `LoadSettings` returns nil in that case.

40. **Settings deny list**: Files and directories matching any deny rule are
    skipped during `walkAndGenerate`. Deny rules may be bare globs
    (`baml_client/**`) or wrapped in `Read(...)` for compatibility with Claude
    Code's permission syntax. A `prefix/**` pattern skips the prefix directory
    itself and all paths beneath it.

41. **Settings are read-only during analysis**: `LoadSettings` never modifies
    any file. Settings only affect which files are walked, never the output
    format or bundle schema.

## Obsidian Vault Invariants

42. **Vault directory structure**: `GenerateObsidianVault` always creates
    subdirectories `packages/`, `state-domains/`, `trust-zones/`, and
    `concurrency-domains/` within `outputDir`, even when those sections are
    empty in the model.

43. **Wiki link format**: All cross-references between notes use
    `[[path/to/note|display text]]` with no `.md` extension in the path
    component. Note paths are relative to `outputDir`.

44. **Vault idempotency**: Running `GenerateObsidianVault` twice on the same
    model with the same `outputDir` produces byte-identical files on both runs.

45. **Filename sanitization**: Note filenames are derived from identifiers by
    replacing `/` and `.` with `-`, collapsing consecutive `-` to one, and
    trimming leading/trailing `-`.

46. **Vault is derived**: The vault is generated from `system_model.yaml`; notes
    are overwritten on each re-generation and must never be manually edited.

## Evidence Enrichment Invariants

47. **Constructors are functions returning package-local types**: `symbols.constructors`
    lists every top-level function (not method) whose return types include at least
    one type declared in the same file. Sorted lexicographically. Absent (omitempty)
    when empty.

48. **Struct fields captured**: For struct TypeDecls, `fields` contains one entry per
    exported field in declaration order. Embedded exported types appear using their
    base type name as the field name. Non-struct kinds have no `fields` entry
    (omitempty). Unexported fields are never included.

49. **Serialization format signals**: `signals.yaml_io` is true when the file imports
    a path containing "yaml" (e.g. `gopkg.in/yaml.v3`) or calls a `yaml.*` target.
    `signals.json_io` is true when the file imports `encoding/json` or calls a
    `json.*` target. Both are purely static (INV-18).

## Skip / Cache Invariants

50. **Evidence bundle skip**: `WriteEvidenceBundle` and `writeBundleAt` compare the
    SHA256 of the new bundle against `file.sha256` in any existing companion
    `.evidence.yaml`. If the hashes match and `force` is false, writing is skipped
    and `skipped=true` is returned. The on-disk file is not touched. `WalkAndGenerate`
    propagates this into separate `written` and `skipped` return counts.

51. **System model skip**: `runSystemModel` calls `SystemModelUpToDate(root, outputPath)`
    before invoking the LLM. If the existing `system_model.yaml` has an
    `inputs.bundle_set_sha256` that matches `computeBundleSetHash` of the current
    evidence bundles, generation is skipped entirely and a "up to date" message is
    printed. If `force` is true this check is bypassed and the model is always
    regenerated.

52. **Force flag**: Both `iguana analyze` and `iguana system-model` accept `--force`
    (`-f`). When present, skip checks (INV-50, INV-51) are bypassed and outputs are
    always regenerated. The flag may appear anywhere in the argument list.
