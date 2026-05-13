# Midstream Build Tags and Companion File Rules

These rules apply to **Go source files** (`*.go`, `*_test.go`). Skip this rule for non-Go file diffs.

This repository is a midstream fork of kserve/kserve running on OpenShift (OCP). Platform-specific
code must be isolated using Go build tags so upstream syncs stay conflict-free. OCP-specific logic
lives in `*_ocp.go` files compiled only with `//go:build distro`; the upstream fallback lives in
`*_default.go` files compiled with `//go:build !distro`.

## Violations - flag as blocking

1. **Missing build tag on OCP imports** - If a file imports packages whose import path contains
   `openshift/`, `opendatahub/`, or `istio.io/`, it must have `//go:build distro` before the `package`
   declaration. Flag if the header is absent.

2. **`*_ocp.go` without build tag** - Any file named `*_ocp.go` or `*_ocp_test.go` must have
   `//go:build distro` before the `package` declaration. Flag if missing.

3. **`*_default.go` without build tag** - Any file named `*_default.go` must have
   `//go:build !distro` before the `package` declaration. Flag if missing.

4. **Commented-out code blocks** - Commented-out function bodies, struct fields, type definitions,
   or conditional branches are a violation. They rot, cause merge conflicts, and are never
   re-enabled. Use `//go:build` compile-time exclusion instead. Flag any `//` comment that wraps
   meaningful code (not documentation or inline explanation). Suggest the author remove the commented
   block or move it behind a `//go:build` constraint instead.

5. **OCP logic in non-companion file** - If a file contains OCP-specific imports or logic (signals:
   `openshift/`, `opendatahub/`, `istio.io/` import paths) and is not named `*_ocp.go` or
   `*_ocp_test.go`, flag it. Suggest extracting the OCP-specific parts to a `<basename>_ocp.go`
   companion with a `<basename>_default.go` with `//go:build !distro` and stub implementations of the same function signatures.

6. **Missing default companion** - If a `*_ocp.go` file is added in this PR but no corresponding
   `*_default.go` exists in the same package (check both the PR diff and the existing repo tree),
   flag it. Upstream builds without `GOTAGS=distro` will fail to link. If the reviewer cannot inspect the
   full repository tree (diff-only mode), flag tentatively and ask the author to confirm whether a
   `*_default.go` companion exists in the same package.

## Build system propagation rules

These rules apply when the diff touches **Dockerfiles**, **Makefile targets**, **Tekton PipelineRuns**,
or any other CI/build system file that builds Go binaries importing `pkg/scheme` or any
`//go:build distro` gated code.

The core principle: `GOTAGS=distro` must flow unbroken from the build system into `go build`.
Every layer in the chain is a potential break point.

7. **Dockerfile missing GOTAGS support** - Any Dockerfile that compiles a Go binary which imports
   `pkg/scheme` (or any package with `//go:build distro` companion files) must declare `ARG GOTAGS`
   in the builder stage and pass `-tags "${GOTAGS}"` to `go build`. Without this, `*_ocp.go` files
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

- Files under a `distro/` sub-package (e.g. `pkg/controller/.../distro/controller_rbac_ocp.go`)
  that contain no executable code - package declaration, license/copyright header, explanatory
  comments, and `//+kubebuilder:rbac:` markers are all fine. These intentionally have no build tag
  and are not named `*_ocp.go` - `controller-gen` must scan them in all build configurations.
  Exempt from violations #2, #3, and #5.
