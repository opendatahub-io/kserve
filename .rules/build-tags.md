# Midstream Build Tags and Companion File Rules

Violations 1-6 apply to **Go source files** (`*.go`, `*_test.go`) - skip them for non-Go diffs.
Rules 7-8 (GOTAGS build system propagation) apply to **Dockerfiles, Makefiles, and Tekton/CI
pipeline files** - always check those regardless of whether Go files are in the diff.

## Context - why these rules exist

This repository is a midstream fork of `kserve/kserve` running on OpenShift. Upstream syncs
happen regularly. Every inline edit to an upstream-owned file is a potential merge conflict during
sync. The core principle: **midstream changes live in companion files, never inline in upstream
code.**

There are two companion file patterns. Both use the `_odh` suffix:

1. **Hook pairs** (`*_odh.go` + `*_default.go`): The upstream file calls a hook function.
   `*_default.go` (`//go:build !distro`) provides the upstream no-op or passthrough.
   `*_odh.go` (`//go:build distro`) provides the midstream implementation. Canonical example:
   `controller_setup_odh.go` / `controller_setup_default.go` implementing `extendControllerSetup`.

2. **Additive-only files** (`*_odh.go`): New midstream-only logic that has no upstream equivalent
   and does not need a `*_default.go` counterpart - e.g. new constants, new helper packages, test
   fixtures. Carry `//go:build distro` if they import OCP-specific packages.
   Example: `constants_odh.go`.

The key test: **does the upstream file need to change at all?** If yes, the only acceptable
change is adding a hook call. All platform-specific logic belongs in the companion files.

### Naming convention

All midstream companion files use the `_odh.go` suffix (e.g. `router_odh.go`,
`controller_setup_odh.go`).

## Violations - flag as blocking

1. **Missing build tag on OCP imports** - If a file imports packages whose import path contains
   `openshift/`, `opendatahub/`, or `istio.io/`, it must have `//go:build distro` before the `package`
   declaration. Flag if the header is absent.

2. **`*_odh.go` without build tag** - Any file named `*_odh.go` or
   `*_odh_test.go` that imports OCP-specific packages (`openshift/`,
   `opendatahub/`, `istio.io/`) must have `//go:build distro` before the `package` declaration.
   Flag if missing. Pure-additive `*_odh.go` files that do not import OCP packages (e.g.
   `constants_odh.go`) do not strictly require the tag, but it is recommended.

3. **`*_default.go` without build tag** - Any file named `*_default.go` must have
   `//go:build !distro` before the `package` declaration. Flag if missing.

4. **Commented-out code blocks** - Commented-out function bodies, struct fields, type definitions,
   or conditional branches are a violation. They rot, cause merge conflicts, and are never
   re-enabled. Use `//go:build` compile-time exclusion instead. Flag any `//` comment that wraps
   meaningful code (not documentation or inline explanation). Suggest the author remove the commented
   block or move it behind a `//go:build` constraint instead.

5. **Midstream-specific logic in non-companion file** - If a file is NOT named `*_odh.go`,
   `*_default.go`, `*_odh_test.go`, and is NOT under a `distro/`
   sub-package path, it must not contain midstream-specific additions. This is the most important
   violation to catch - it is the primary source of merge conflicts during upstream syncs.

   Check the diff for these **detection signals** (any single match is sufficient to flag):
   - **Import signals**: import paths containing `openshift/`, `opendatahub/`, or `istio.io/`
   - **Identifier signals**: new or modified constants, variables, types, or function/method names
     containing `odh`, `ocp`, `openshift`, `distro`, or `midstream` (case-insensitive). Examples
     from real PRs that were missed: `ODHS3Endpoint`, `applyHardwareProfile`,
     `applyHardwareProfileToLWS`, `HardwareProfileAnnotationName`
   - **Comment signals**: inline comments containing `ODH`, `OCP`, `OpenShift`, `midstream`, or
     `distro` as descriptive markers. Examples: `// ODH only`, `// midstream-specific`,
     `// OCP-only path`
   - **String literal signals**: string literals containing `opendatahub.io` or `openshift.io`.
     Examples: annotation keys like `"opendatahub.io/hardware-profile-name"`, API group references
   - **Cross-file call signals**: function or method calls where the called function is defined in
     a companion `*_odh.go` file in the same package. If the diff adds a function
     call and the function name suggests midstream-specific behavior (contains `odh`, `ocp`,
     `hardwareProfile`, `openshift`, etc.), flag it even if you cannot verify the definition
     location - the author can confirm.

   When flagging, recommend the appropriate companion file pattern:
   - If the upstream file needs to call the new logic: add a **hook function** - define the
     function signature in a `*_default.go` (no-op, `//go:build !distro`) and provide the real
     implementation in a `*_odh.go` (`//go:build distro`). The upstream file just calls the hook.
     Reference the canonical pattern: `controller.go` calls `extendControllerSetup()`, implemented
     as no-op in `controller_setup_default.go` and as ODH setup in `controller_setup_odh.go`.
   - **Hook functions that need the reconciler must be receiver methods** - When a hook is called
     from a reconciler and the distro implementation needs API client access (via the reconciler),
     define the hook as a receiver method on the reconciler type, not as a standalone function
     taking the reconciler as a parameter. This is idiomatic Go and avoids threading the reconciler
     as an extra parameter through the call chain.
     Correct: `func (c *MyReconciler) enhanceJob(ctx context.Context, job *batchv1.Job) error`
     Wrong: `func enhanceJob(ctx context.Context, c *MyReconciler, job *batchv1.Job) error`
   - If the logic is purely additive (new constants, new helper functions not called from
     upstream): extract to an additive `*_odh.go` or `constants_odh.go` file.

6. **Missing default companion** - If a `*_odh.go` file is added in this PR that
   implements a hook function called from a non-companion file, but no corresponding `*_default.go`
   exists in the same package (check both the PR diff and the existing repo tree), flag it.
   Upstream builds without `GOTAGS=distro` will fail to link. If the reviewer cannot inspect the
   full repository tree (diff-only mode), flag tentatively and ask the author to confirm whether a
   `*_default.go` companion exists in the same package.

   Note: additive `*_odh.go` files that only introduce new symbols not called from upstream code
   do NOT require a `*_default.go` companion.

## Build system propagation rules

These rules apply when the diff touches **Dockerfiles**, **Makefile targets**, **Tekton PipelineRuns**,
or any other CI/build system file that builds Go binaries importing `pkg/scheme` or any
`//go:build distro` gated code.

The core principle: `GOTAGS=distro` must flow unbroken from the build system into `go build`.
Every layer in the chain is a potential break point.

7. **Dockerfile missing GOTAGS support** - Any Dockerfile that compiles a Go binary which imports
   `pkg/scheme` (or any package with `//go:build distro` companion files) must declare `ARG GOTAGS`
   in the builder stage and pass `-tags "${GOTAGS}"` to `go build`. Without this, `*_odh.go` files
   are silently skipped - causing missing scheme registrations, CRD watch failures, or runtime
   crashes. Two valid patterns:
   - `ARG GOTAGS=""` when the caller always supplies the value (Makefile / generic CI).
   - `ARG GOTAGS="distro"` when the Dockerfile is exclusively used for distro builds (Konflux).
   Canonical references: `llmisvc-controller.Dockerfile` (Makefile pattern),
   `Dockerfiles/llmisvc-controller.Dockerfile.konflux` (Konflux pattern).

8. **Build system invocation not passing GOTAGS** - Every invocation of a Dockerfile covered by
   rule 7 must pass `GOTAGS=distro` through that build system's mechanism for Docker build
   arguments. The mechanism varies by system - check each caller type in the diff:
   - **Makefile**: `--build-arg GOTAGS=${GOTAGS}` on the `buildx build` call.
     Canonical references: `docker-build` and `docker-build-llmisvc` in `Makefile`.
   - **Tekton PipelineRun** (`.tekton/*.yaml`): `build-args: ["GOTAGS=distro"]` in `spec.params`.
     Canonical reference: `odh-kserve-llmisvc-controller-pull-request.yaml` in `.tekton/`.
   - **Konflux Dockerfiles** (`Dockerfiles/*.Dockerfile.konflux`): prefer `ARG GOTAGS="distro"`
     as the default so the pipeline does not need to pass it explicitly.
     Canonical reference: `Dockerfiles/llmisvc-controller.Dockerfile.konflux`.
   If a new build system is introduced, apply the same principle: locate the equivalent of
   `--build-arg` for that system and verify GOTAGS reaches the compiler.

## Exemptions - do not flag

- Files under a `distro/` sub-package (e.g. `pkg/controller/.../distro/controller_rbac_odh.go`)
  that contain no executable code - package declaration, license/copyright header, explanatory
  comments, and `//+kubebuilder:rbac:` markers are all fine. These intentionally have no build tag
  - `controller-gen` must scan them in all build configurations.
  Exempt from violations #2, #3, and #5.
