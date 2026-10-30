# KServe (ODH Midstream)

ODH midstream fork of [kserve/kserve](https://github.com/kserve/kserve). Go controllers via controller-runtime. See [docs/architecture.md](docs/architecture.md) for reconciliation flows and the distro build-tag pattern.

## Constraints

- **Generated files are read-only** — overwritten by `make precommit`: `charts/*/`, quick-install scripts, Helm helpers synced from `charts/_common/`
- **Makefile is source of truth** — read `Makefile` / `Makefile.tools.mk` before changing build steps; midstream overrides go in `Makefile.overrides.mk` only
- **ODH logic in companion files** — never inline in upstream-owned files; see below and [architecture.md](docs/architecture.md#distro-build-tag-pattern)
- **Run `make precommit` before committing**

## ODH-specific changes

| Change | Location |
|--------|----------|
| ODH/OpenShift behavior | `*_odh.go` (`//go:build distro`) |
| Upstream no-op fallback | `*_default.go` (`//go:build !distro`) |
| ODH-only RBAC | `distro/controller_rbac_odh.go` (generated via `make manifests-distro`) |
| Makefile / image names | `Makefile.overrides.mk` |
| Scheme registration | `pkg/scheme/register_odh.go`, `cmd/manager/main_schemes_odh.go` |

**Hook pairs** — upstream calls a hook; `_default.go` no-ops, `_odh.go` implements. Example: `controller_setup_{default,odh}.go` in llmisvc. Only acceptable upstream edit is adding the hook call; use reconciler receiver methods when the hook needs client access.

**Additive-only** — new ODH symbols in `*_odh.go` with no `_default.go` when upstream never calls them.

`Makefile.overrides.mk` sets `GOTAGS=distro`. Propagate through Dockerfiles/build targets — see `.rules/{build-tags,distro-builds,makefile-split}.md`.

## Layout

- APIs: `pkg/apis/serving/{v1alpha1,v1alpha2,v1beta1}`
- Controllers: `pkg/controller/{v1alpha1,v1alpha2,v1beta1}`
- Webhooks: `pkg/webhook/admission/`
- Binaries: `cmd/manager/` (ISVC, InferenceGraph), `cmd/llmisvc/`, `cmd/localmodel/` (ModelCache)

## Commands

`make help` lists the targets you run directly, when to run each and what it rewrites or needs.

Prerequisites: Go (version pinned in `go.mod`) and `make`.

```
make test          # full Go test suite
make precommit     # format, lint, codegen, manifest sync
```

To iterate, run a subset with `make test-focus`. It uses the envtest assets of `make test` but skips its `fmt`, `vet`, `manifests` and `qpext` steps, and it fails when no test or spec ran:

```
make test-focus PKG=./pkg/utils RUN='^TestUnionUtil$'
make test-focus PKG=./pkg/controller/v1alpha2/llmisvc RUN=TestLLMInferenceServiceController FOCUS='TLS Toggle'
```

`PKG` takes package patterns, `RUN` a `go test -run` regex and `FOCUS` a Ginkgo focus regex. With `FOCUS`, packages without a Ginkgo suite are skipped. A Ginkgo suite runs from one `Test*` function, so naming it in `RUN` skips the plain tests in the same package. After an API type change, run `make manifests` first: envtest loads the CRDs from `test/crds/`.

## Testing

- Tests colocated with code; unit tests use `fake.NewClientBuilder`, controllers use envtest
- Use `pkgtest.NewEnvTest()` from `pkg/testing/` (not raw `envtest.Environment{}`); llmisvc has `fixture/` builders
- envtest has no built-in controllers or GC — simulate external status updates yourself
- Per-test namespace, `defer` cleanup, `Eventually`/`Consistently` (never `time.Sleep`), `retry.RetryOnConflict` for contested updates
- Match the test package style of the controller you're editing (llmisvc uses external `_test` packages)

## Controller conventions (this repo)

- Reconcile idempotently; `NotFound` → success; guard status writes with deep-equal to avoid loops
- `spec` vs `status` in separate API calls; use `Mark*` helpers from `*_lifecycle.go` (see LLMISVC as reference), not raw condition slice edits
- Include `observedGeneration` in conditions; composite `Ready` should surface first failing sub-condition
- ODH networking/permissions changes → companion `*_odh.go` files listed in [architecture.md](docs/architecture.md)

## Pull requests

Before you write a PR title or description, read `.github/PULL_REQUEST_TEMPLATE.md`. Its comments are the rules: follow them, then delete every one. `.github/PR_DESCRIPTION_EXAMPLES.md` shows the right size.
