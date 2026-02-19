# Evidence Bundle — Invariants

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
    walking. Test files (`*_test.go`) are also skipped.

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
    `bundle:<path>@v<version>[#symbol:<name>|#signal:<name>]`
    — no other formats are valid.

31. **bundle_set_sha256 derivation**: `inputs.bundle_set_sha256` is a SHA256
    hash derived from all loaded bundle paths and hashes, sorted and joined by
    newline. It changes whenever any bundle is added, removed, or modified.
