# Distro Build Propagation Rules

These rules apply when the diff touches **Dockerfiles**, **Makefile targets**, **Tekton PipelineRuns**,
or any other CI/build system file that builds Go binaries from this repository.

## Context - why GOTAGS must flow through every build layer

Midstream code lives behind `//go:build distro` companion files (see `build-tags.md` for companion
file rules). These files are compiled only when `-tags distro` is passed to `go build`. The build
tag must flow unbroken from the outermost build system (Makefile, Tekton, Konflux) through Docker
build-args into the `go build` command. Every layer in the chain is a potential break point - and
failures are silent: the binary compiles fine but ships without midstream scheme registrations,
CRD watches, or runtime behavior. The symptom is a runtime crash or missing functionality, not a
build error.

The key transitive dependency: any binary whose `cmd/` package imports `pkg/scheme` pulls in
`pkg/scheme/register_odh.go` (`//go:build distro`), which registers OpenShift/ODH API types.
Without the tag, those types are never registered and the controller crashes on first reconcile.

## Violations - flag as blocking

1. **Dockerfile missing GOTAGS support** - Any Dockerfile that compiles a Go binary from this
   repository must declare `ARG GOTAGS` in the builder stage and pass `-tags "${GOTAGS}"` to
   `go build`. Without this, `*_odh.go` files are silently skipped. Two valid default patterns:
   - `ARG GOTAGS=""` when the caller supplies the value (Makefile / generic CI).
   - `ARG GOTAGS="distro"` when the Dockerfile is exclusively used for distro builds (Konflux).
   Canonical references: `llmisvc-controller.Dockerfile` (Makefile pattern),
   `localmodel-agent.Dockerfile` (Makefile pattern).

2. **Makefile docker-build target not passing GOTAGS** - Every `docker-build-*` target in the
   Makefile that builds a Go Dockerfile must include `--build-arg GOTAGS=${GOTAGS}` on the
   `buildx build` call. `Makefile.overrides.mk` sets `GOTAGS=distro`, but the value only reaches
   the compiler if each target explicitly passes it as a build-arg.
   Canonical references: `docker-build` and `docker-build-llmisvc` targets in `Makefile`.

3. **Tekton PipelineRun missing GOTAGS** - Every `odh-*` PipelineRun in `.tekton/` that builds a
   Go image must include `build-args: ["GOTAGS=distro"]` in `spec.params`. The upstream
   `kserve-*` PipelineRuns also pass GOTAGS for consistency.
   Canonical reference: `odh-kserve-llmisvc-controller-pull-request.yaml`.

4. **Konflux Dockerfile without GOTAGS default** - Dockerfiles under `Dockerfiles/` with the
   `.konflux` suffix are used exclusively for distro builds. They should default to
   `ARG GOTAGS="distro"` so the pipeline does not need to pass it explicitly. If a Konflux
   Dockerfile compiles Go and lacks this default, flag it.
   Note: Konflux Dockerfiles live in the downstream repo (`red-hat-data-services/kserve`), not in
   midstream. This rule applies when reviewing downstream PRs.

5. **New Go binary without GOTAGS audit** - When a PR adds a new `cmd/` entry point, a new
   Dockerfile, or a new Makefile `docker-build-*` target, verify the full chain:
   - Does the Dockerfile declare `ARG GOTAGS` and pass `-tags "${GOTAGS}"`?
   - Does the Makefile target pass `--build-arg GOTAGS=${GOTAGS}`?
   - Does the corresponding Tekton PipelineRun include `build-args: ["GOTAGS=distro"]`?
   - If a Konflux variant exists downstream, does it default to `ARG GOTAGS="distro"`?
   Flag any gap as blocking. A single missing layer silently disables all distro code.

## Exemptions - do not flag

- **`kserve-module-controller.Dockerfile`** - This Dockerfile builds the kserve-module controller,
  which is purely midstream code. There is no upstream/distro build-tag split - all code compiles
  unconditionally. GOTAGS is not needed.

- **Python Dockerfiles** (`python/*.Dockerfile`) - These build Python serving runtimes, not Go
  binaries. GOTAGS does not apply.

- **Sample/doc Dockerfiles** (`docs/samples/**/*.Dockerfile`) - Example Dockerfiles for
  documentation purposes. Not part of the production build pipeline.
