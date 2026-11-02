# Upstream Sync -- Conflict Resolution Policy

This is the canonical conflict-resolution policy for syncing
`kserve/kserve` (upstream) into `opendatahub-io/kserve` (midstream), and
midstream `master` into release branches. It is versioned with this
repository because it changes with the codebase (fork-only paths, build
tags, generated files).

Environment setup and git-write permissions are consumer-specific and are
NOT defined here -- each consumer supplies its own.

## Fork-Specific Identifiers (NEVER remove these)

For full conventions on build tags, companion file patterns, Makefile separation,
and kustomize overlay hygiene, read the `.rules/` directory (especially
`.rules/build-tags.md`, `.rules/makefile-split.md`, `.rules/kustomize-hygiene.md`).

Fork-only paths (never take upstream deletions of these):
- `*_odh.go` files -- compiled with `//go:build distro` (OpenShift/ODH code)
- `*_default.go` files -- compiled with `//go:build !distro` (upstream fallback)
- `Makefile.overrides.mk` -- fork-only Make overrides (sets `GOTAGS=distro`)
- `Makefile.ocp.mk` -- fork-only OCP targets
- `config/overlays/odh/` -- entire directory is fork-only
- `config/overlays/odh-xks/` -- fork-only
- `config/overlays/odh-modelcache/` -- fork-only
- `kserve-module/` -- entire directory is fork-only
- `pkg/constants/constants_odh.go` -- fork-only constants
- `docs/odh/`, `docs/openshift/`, `docs/OPENSHIFT_GUIDE.md` -- fork-only docs
- `test/scripts/openshift-ci/` -- fork-only CI scripts
- `.rules/` -- fork-only review rules
- `.github/upstream-sync-instructions.md` -- this document

If upstream deletes or modifies code that these files depend on, keep the
fork code and adapt it to work with the new upstream structure.

### AUTOMERGE fences

Code blocks fenced with `AUTOMERGE(keep)` / `AUTOMERGE(end)` comments are
fork-specific and MUST be preserved unconditionally during upstream sync:

```go
// AUTOMERGE(keep): <reason>
<fork-specific code>
// AUTOMERGE(end)
```

The comment syntax matches the file type (`//` for Go, `#` for YAML/Makefile,
`<!-- -->` for HTML/Markdown). These fences signal that the enclosed block is
intentionally maintained by this fork -- never remove, reorder, or merge it
with upstream code. If upstream changes create a conflict around or within a
fenced block, resolve by keeping the fenced content intact and integrating
upstream changes around it.

## Merge Conflict Resolution Guidelines

### Understanding the markers

Git conflict markers have this structure:
- Line starting with 7x `<` followed by `HEAD` = our fork's version (midstream master)
- Line with exactly 7x `=` = separator between the two sides
- Line starting with 7x `>` followed by the source ref = the version being merged in

Everything between the opening and separator is ours; between separator and closing is theirs.

### How to approach each conflicted file

1. **Read the entire file** to understand context, not just the conflicted hunks.
2. **Check git log** for context on what changed:
   - `git log --oneline HEAD..SOURCE_REF -- <file>` -- what the source changed
   - `git log --oneline SOURCE_REF..HEAD -- <file>` -- what our fork changed
3. **Determine the nature** of the conflict:
   - Did the source rename/refactor something we also modified?
   - Did both sides add new code in the same region?
   - Is our fork's change a customization or just an older version of the same code?

### Resolution priority (in order)

1. **Preserve all fork-specific customizations.** Look for any code
   unique to this fork (ODH-specific, OpenDataHub, OpenShift, Red Hat,
   or any non-upstream additions). These MUST be kept. Code within
   `AUTOMERGE(keep)` / `AUTOMERGE(end)` fences is unconditionally preserved.
2. **Accept source changes** for code that has no fork-specific modifications.
   If our side is just an older version of what the source now has, take the source.
3. **Integrate both sides** when both source and fork modified the same
   code. Apply the source refactor/rename while keeping fork additions.
4. **Imports**: include all imports needed by both sides. Remove duplicates.
5. **go.mod / go.sum**: accept source dependency versions unless there is
   a `replace` directive with a comment explaining a specific pin.
6. **Config/YAML**: accept source structural changes to base configs.
   Keep any fork-specific overlay patches intact.

### Common patterns

- **Source renamed a function/type we use**: adopt the new name everywhere,
  including in our fork-specific code that references it.
- **Both sides added entries to a list** (e.g. imports, RBAC rules, env vars):
  keep both sets, deduplicate, maintain alphabetical order if the file uses it.
- **Source deleted code our fork still uses**: keep our fork's code.
- **Source added code in a region we also added code**: keep both additions,
  order them logically (source first, then fork additions).

### Special file handling

- **go.sum**: do NOT manually resolve. Delete the file entirely, then run
  `go mod tidy` to regenerate it.
- **go.mod `replace` directives**: preserve ALL replace directives during
  automated sync. Determining whether upstream has resolved the underlying
  issue (e.g. a CVE) requires human verification -- do not drop replaces
  during conflict resolution.
- **go.mod `AUTOMERGE(keep)` blocks**: the distro-only `require` block is
  fenced and must be preserved as-is. Do not merge its deps into the main
  require block.
- **Generated files** (CRDs, deepcopy, RBAC manifests): do NOT manually edit.
  Resolve conflicts in the SOURCE files only -- `make precommit` will
  regenerate all derived files automatically. If you need to verify a build
  mid-resolution, `make manifests generate` can be run explicitly.
- **uv.lock / lock files**: do NOT manually resolve. Remove conflict markers,
  then `make uv-lock` will regenerate.
- **`*_odh.go` / `*_default.go` pairs**: these are always fork-only.
  If upstream changes the non-`_odh` file's signatures, update the `_odh.go`
  and `_default.go` companions to match. See `.rules/build-tags.md` for the
  full companion file conventions.
- **`.tekton/` pipelines**: when syncing into a release/stable branch, keep
  the target's version -- these files are managed per-branch by the
  odh-konflux-central onboarder. When syncing upstream/master into
  odh/master, take the source version.

### If a verification command fails

- Build/compile failures: read the error, fix the code. Common causes are
  missing imports, renamed types, or wrong function signatures.
- Formatting/linting failures: these are usually auto-fixable by the tool
  itself (e.g. `make`-based precommit targets, `gofmt`, linters with a
  `--fix`/`--write` flag) -- let it auto-fix, then re-run rather than
  hand-editing to match the formatter.

## Verification (MUST do all in order after resolving)

1. Ensure all conflicts are properly resolved (both sides integrated correctly)
   and no conflict markers remain (the 7-character `<`, `=`, `>` lines).
   Do NOT simply delete marker lines -- the code between them must be resolved.
2. Verify no markers remain: run `git grep` for the opening marker (7x less-than
   followed by a space). Must return no results (exit code 1).
3. Run the build verification for this repository:
   - `go build ./...`
   - `cd qpext && go build ./...`
   - `go build -tags distro ./...` (catches broken `_odh.go`/`_default.go` pairs)
   - `go vet ./...`
   - `make precommit`
   If your consumer supplies its own verification commands, run those and do
   not substitute your own checks for them.
