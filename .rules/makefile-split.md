# Midstream Makefile Split Rules

These rules apply to **`Makefile` only** (exact filename match). Do not apply to
`Makefile.overrides.mk`, `Makefile.tools.mk`, or any other `Makefile.*` variant - those are either
upstream-owned tooling or the intended home for midstream content.

## Context - why the Makefile must stay clean

The upstream `Makefile` includes `-include Makefile.overrides.mk` at the top, giving midstream a
clean extension point. `Makefile.overrides.mk` can override variables, add new targets, and
append to existing ones without touching the upstream file. This is where `GOTAGS=distro` is set,
which activates all build-tagged midstream code.

Any midstream-specific content added directly to `Makefile` will conflict on every upstream sync.
The override file is the only place for midstream build logic.

## Violations - flag as blocking

1. **Distro-specific targets or variables in `Makefile`** - If a new make target, variable
   assignment, or conditional block is OCP/OpenShift/distro-specific (signals: references to
   `distro`, `openshift`, `OCP`, `odh`, or OpenShift-specific tooling (`oc`, `opm`, `rosa`), or distro-specific image registries (`registry.redhat.io`, `registry.access.redhat.com`, `quay.io/rhoai`, `quay.io/rhods`)), flag it and suggest moving
   it to `Makefile.overrides.mk`.

2. **`GOTAGS=distro` or build tag references** - Any addition of `GOTAGS=distro` or references to
   `//go:build distro` directly in `Makefile` belongs in `Makefile.overrides.mk`, where it is
   already set.

3. **Modifying existing upstream targets or variables** - If a change appends distro-specific flags
   to an existing upstream make target (e.g. adding `-tags distro` to a build target, appending to
   `$(MAKE) test`, overriding a variable already defined upstream), flag it and suggest using
   `Makefile.overrides.mk` override syntax instead.

4. **New `-include` directives** - A new `-include` line may introduce a useful extension point.
   Ask the author (in the review thread): is this generally useful and worth proposing to upstream
   kserve/kserve? A change is generally useful if it is not OCP/ODH-specific, does not reference
   distro registries or credentials, and adds an extension point any downstream project could benefit
   from. If yes, ask the author to confirm an upstream proposal is planned or filed. If
   midstream-only, suggest moving it to `Makefile.overrides.mk` instead. Block merge until the
   author confirms intent in the PR description or review thread.
