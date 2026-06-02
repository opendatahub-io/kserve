# Upstream Sync — Conflict Resolution Instructions

## Environment (pre-installed — do NOT install these yourself)

- Go (version matching go.mod) with modules pre-downloaded
- Python 3.11 with venv
- All Make tools pre-installed in `./bin/`:
  golangci-lint, controller-gen, kustomize, yq, helm-docs, pinact
- Python tools in `./bin/.venv/bin/`: uv, ruff
- Do NOT run `pip install`, `go install`, or download any tools manually.
  Just use `make` targets directly.

## Fork-Specific Identifiers (NEVER remove these)

This fork uses a **build-tag architecture** for platform-specific code:
- `*_ocp.go` files → compiled with `//go:build distro` (OpenShift/ODH code)
- `*_default.go` files → compiled with `//go:build !distro` (upstream fallback)
- `Makefile.overrides.mk` → fork-only Make overrides (sets `GOTAGS=distro`)
- `Makefile.ocp.mk` → fork-only OCP targets
- `config/overlays/odh/` → entire directory is fork-only
- `config/overlays/odh-xks/` → fork-only
- `config/overlays/odh-modelcache/` → fork-only
- `kserve-module/` → entire directory is fork-only
- `pkg/constants/constants_odh.go` → fork-only constants
- `docs/odh/`, `docs/openshift/`, `docs/OPENSHIFT_GUIDE.md` → fork-only docs
- `test/scripts/openshift-ci/` → fork-only CI scripts
- `.rules/` → fork-only review rules

If upstream deletes or modifies code that these files depend on, keep the
fork code and adapt it to work with the new upstream structure.

### AUTOMERGE fences

Code blocks fenced with `AUTOMERGE(keep)` / `AUTOMERGE(end)` comments are
fork-specific and MUST be preserved unconditionally during upstream sync:

```
// AUTOMERGE(keep): <reason>
<fork-specific code>
// AUTOMERGE(end)
```

The comment syntax matches the file type (`//` for Go, `#` for YAML/Makefile,
`<!-- -->` for HTML/Markdown). These fences signal that the enclosed block is
intentionally maintained by this fork — never remove, reorder, or merge it
with upstream code. If upstream changes create a conflict around or within a
fenced block, resolve by keeping the fenced content intact and integrating
upstream changes around it.

## Merge Conflict Resolution Guidelines

### Understanding the markers

Git conflict markers have this structure:
- Line starting with 7x `<` followed by `HEAD` = our fork's version (midstream master)
- Line with exactly 7x `=` = separator between the two sides
- Line starting with 7x `>` followed by `upstream/master` = upstream's version (kserve/kserve master)

Everything between the opening and separator is ours; between separator and closing is theirs.

### How to approach each conflicted file

1. **Read the entire file** to understand context, not just the conflicted hunks.
2. **Check git log** for context on what changed:
   - `git log --oneline HEAD..upstream/master -- <file>` — what upstream changed
   - `git log --oneline upstream/master..HEAD -- <file>` — what our fork changed
3. **Determine the nature** of the conflict:
   - Did upstream rename/refactor something we also modified?
   - Did both sides add new code in the same region?
   - Is our fork's change a customization or just an older version of the same code?

### Resolution priority (in order)

1. **Preserve all fork-specific customizations.** Look for any code
   unique to this fork (ODH-specific, OpenDataHub, OpenShift, Red Hat,
   or any non-upstream additions). These MUST be kept. Code within
   `AUTOMERGE(keep)` / `AUTOMERGE(end)` fences is unconditionally preserved.
2. **Accept upstream changes** for code that has no fork-specific modifications.
   If our side is just an older version of what upstream now has, take upstream.
3. **Integrate both sides** when both upstream and fork modified the same
   code. Apply the upstream refactor/rename while keeping fork additions.
4. **Imports**: include all imports needed by both sides. Remove duplicates.
5. **go.mod / go.sum**: accept upstream dependency versions unless there is
   a `replace` directive with a comment explaining a specific pin.
6. **Config/YAML**: accept upstream structural changes to base configs.
   Keep any fork-specific overlay patches intact.

### Common patterns

- **Upstream renamed a function/type we use**: adopt the new name everywhere,
  including in our fork-specific code that references it.
- **Both sides added entries to a list** (e.g. imports, RBAC rules, env vars):
  keep both sets, deduplicate, maintain alphabetical order if the file uses it.
- **Upstream deleted code our fork still uses**: keep our fork's code.
- **Upstream added code in a region we also added code**: keep both additions,
  order them logically (upstream first, then fork additions).

### Special file handling

- **go.sum**: do NOT manually resolve. Delete the file entirely, then run
  `go mod tidy` to regenerate it.
- **go.mod `replace` directives**: keep replace directives that have a
  comment explaining a specific pin. If upstream has already fixed the issue
  a replace was addressing (e.g. a CVE fix was merged), the replace can be
  dropped.
- **go.mod `AUTOMERGE(keep)` blocks**: the distro-only `require` block is
  fenced and must be preserved as-is. Do not merge its deps into the main
  require block.
- **Generated files** (CRDs, deepcopy, RBAC manifests): do NOT manually edit.
  Resolve conflicts in the SOURCE files only — `make precommit` will
  regenerate all derived files automatically. If you need to verify a build
  mid-resolution, `make manifests generate` can be run explicitly.
- **uv.lock / lock files**: do NOT manually resolve. Remove conflict markers,
  then `make uv-lock` will regenerate.
- **`*_ocp.go` / `*_default.go` pairs**: these are always fork-only.
  If upstream changes the non-`_ocp` file's signatures, update the `_ocp.go`
  and `_default.go` companions to match.

### If something fails

- If `go build` fails after resolution: read the error, fix the code. Common
  causes are missing imports, renamed types, or wrong function signatures.
- If `make precommit` fails: read the output, fix the issue, re-run.
  Formatting/linting errors are usually auto-fixable by the make targets themselves.

## Verification (MUST do all in order after resolving)

1. Ensure all conflicts are properly resolved (both sides integrated correctly)
   and no conflict markers remain (the 7-character `<`, `=`, `>` lines).
   Do NOT simply delete marker lines — the code between them must be resolved.
2. Verify no markers remain: run `git grep` for the opening marker (7x less-than
   followed by a space). Must return no results (exit code 1).
3. Run: `go build ./...` — must succeed.
4. Run: `cd qpext && go build ./...` — must succeed.
5. Run: `make precommit` — let it auto-fix any formatting issues.

## IMPORTANT: Do NOT use git write commands

Do NOT run `git add`, `git commit`, or `git push`.
The workflow handles all git operations after you finish.
You only have read-only git access (log, diff, grep, show).
