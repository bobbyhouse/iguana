# iguana v2 — Invariants

These invariants must hold before and after every change to the implementation.

## Container Directory Invariants

1. **Container root**: Every iguana container lives at `~/.iguana/<name>/`.
   The name is an arbitrary identifier chosen at `iguana init` time.

2. **Container uniqueness**: `iguana init <name>` errors immediately if
   `~/.iguana/<name>/` already exists. It never overwrites an existing container.

3. **Project config location**: Each project is represented by a single YAML
   file at `~/.iguana/<container>/<project>.yaml`. No other files at that level
   are treated as project configs.

4. **Evidence output location**: Plugin evidence bundles are written to
   `~/.iguana/<container>/<project>/<plugin>/`. The plugin's `Analyze` method
   receives this exact path as `outputDir`.

## Project YAML Invariants

5. **Project YAML schema**: A project config file marshals/unmarshals as:
   ```yaml
   plugins:
     <plugin-name>:
       <key>: <value>
   ```
   The top-level key is always `plugins`. Each sub-key is a plugin name
   mapping to a flat string→string config map.

6. **Project uniqueness**: `container.AddProject` errors if `<project>.yaml`
   already exists. It never overwrites an existing project.

7. **Project listing**: `container.ListProjects` returns project names derived
   solely from `*.yaml` filenames in the container directory. Non-`.yaml` entries
   and subdirectories are ignored.

## Plugin Interface Invariants

8. **Plugin.Name stability**: A plugin's `Name()` return value must never change.
   It is used as a directory name and as the YAML key in project configs.

9. **Configure purity**: `Configure()` returns questions only — it does not
   prompt, write files, or perform I/O. It may return an error if the plugin
   cannot determine its questions.

10. **Analyze contract**: `Analyze(config, outputDir)` must:
    - Create `outputDir` if it does not exist.
    - Write evidence bundles as files under `outputDir`.
    - Be idempotent: a second call with the same config and unchanged source
      produces identical output (or skips up-to-date files).
    - Return a non-nil error if any fatal condition prevents analysis.

11. **Plugin registry**: The `plugins` map in `cmd/iguana/main.go` is the
    single source of truth for available plugins. Plugin names in project YAMLs
    that are not present in the registry produce a warning and are skipped
    (not a fatal error).

## Evidence Bundle Format Invariants

12. **Bundle file format**: Every evidence bundle is a markdown file with YAML
    frontmatter delimited by `---\n` on its own line. The frontmatter is valid
    YAML. The body (after the closing `---`) may be empty.

13. **Required frontmatter fields**: Every bundle produced by the static plugin
    must include `plugin`, `schema`, `hash`, `file`, `package`, and `signals`.
    The `functions` and `types` arrays may be absent (omitempty) when empty.

14. **plugin field value**: The `plugin` field is always the plugin's `Name()`
    return value (e.g. `"static"`).

15. **schema field value**: The `schema` field is always the filename of the
    schema document copied into `outputDir` by `Analyze` (e.g. `"schema.md"`).

16. **Schema file copy**: `Analyze` copies `schema.md` to `outputDir/schema.md`
    on every run, overwriting any existing copy.

## Hash and Staleness Invariants

17. **hash correctness**: The `hash` field contains the lowercase hex-encoded
    SHA-256 digest of the source file bytes at the time of bundle creation.

18. **Staleness check**: Before writing a bundle, `Analyze` reads the existing
    bundle file (if any), extracts its `hash` field, and skips writing if the
    hash matches the current source file hash. The on-disk file is not touched
    when skipped.

19. **No timestamps**: Bundle files must not contain timestamps, UUIDs, or
    other environment-dependent values beyond `hash` and `ref`.

## Git Source Fetching Invariants

20. **Clone location**: The repository is cloned to a path derived from a hash
    of the repository URL inside `os.TempDir()`. The clone is not removed after
    analysis.

21. **Shallow clone**: Fresh clones use `git clone --depth 1`. Subsequent
    invocations with the same URL detect the existing `.git/` directory and run
    `git pull --depth 1 --ff-only` instead.

22. **Commit hash capture**: After clone/pull, `git rev-parse HEAD` is run in
    the repo directory to obtain the full 40-character commit SHA. This value is
    embedded in all `ref` URLs.

## Ref URL Format Invariants

23. **Ref URL format**: Source references use the scheme:
    ```
    git://<host>/<org>/<repo>@<commit>/<path/to/file.go>
    git://<host>/<org>/<repo>@<commit>/<path/to/file.go>#L<line>
    ```
    where `<commit>` is the full 40-character SHA-1, `<path>` uses forward
    slashes, and the line number (when present) is 1-based.

24. **Trailing `.git` stripped**: Repository URLs ending in `.git` have that
    suffix removed before constructing `ref` URLs.

25. **Protocol normalised**: `https://` and `http://` prefixes are stripped from
    the repository URL when building `git://` refs.

## Ordering Invariants

26. **Imports sorted**: `package.imports` is sorted lexicographically by `path`.

27. **Functions sorted**: `functions` array is sorted by `name`.

28. **Types sorted**: `types` array is sorted by `name`.

29. **Struct fields in declaration order**: `fields` within a `typeDecl` are
    listed in source declaration order, not alphabetically.

30. **Deterministic walk order**: Source directories and files within each
    directory are processed in sorted (lexicographic) order.

## Directory Walk Invariants

31. **Skipped directories**: The following are always skipped during directory
    walking: `vendor/`, `testdata/`, `examples/`, `docs/`, and any directory
    whose name begins with `.`. Test files (`*_test.go`) are also skipped.

32. **One package load per directory**: `loadPackageForDir` is called at most
    once per unique directory during a single `Analyze` invocation.

33. **Relative paths in bundles**: `file.path` in the bundle frontmatter is
    always the forward-slash path relative to the repository root — never an
    absolute path.

## Signal Invariants

34. **Signals are purely static**: Signals are derived only from imports, AST
    nodes, and call targets — never from runtime state or network access.

35. **Signal monotonicity**: Adding more code to a file can only turn signals
    from `false` to `true`, never from `true` to `false`.

## CLI Command Invariants

36. **Commands slice is the single source of truth**: All registered subcommands
    are in the `commands` slice in `cmd/iguana/main.go`. Dispatch, help listing,
    and `iguana help <cmd>` all derive from this slice.

37. **Help flags**: `iguana`, `iguana --help`, and `iguana -h` all produce the
    same overall usage listing and exit 0.

38. **Unknown subcommand error**: When `os.Args[1]` is not a known command name,
    the process exits non-zero with a message suggesting `iguana help`.

39. **Per-command usage on bad args**: When a subcommand receives wrong or
    missing arguments, it returns a usage error. It does not panic.

40. **init idempotency guard**: `iguana init <name>` never silently overwrites
    an existing container. It always errors on collision.

41. **analyze continues on errors**: `iguana analyze` reports per-plugin errors
    to stderr but continues processing remaining projects and plugins. It returns
    a non-zero exit only after all projects have been attempted.
