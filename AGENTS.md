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

```
make test          # full Go test suite
make test-qpext    # qpext tests (separate Go module under qpext/)
make precommit     # format, lint, codegen, manifest sync
```

Focused test after `make setup-envtest`:

```
KUBEBUILDER_ASSETS="$(./bin/setup-envtest use $(go list -m -f '{{ .Version }}' k8s.io/api | awk -F'[v.]' '{printf "1.%d", $3}') -p path)" \
  go test ./pkg/controller/v1beta1/... -run TestName -v
```

## Testing

- Tests live next to the code they test
- Controller tests mix unit tests (`fake.NewClientBuilder`) and envtest integration suites
- Prefer table-driven tests for pure logic, envtest for controller wiring
- Most controllers use same-package tests. llmisvc uses `_test` packages for integration tests. Follow the convention of whichever controller you're modifying

### envtest

envtest runs a real API server and etcd but NOT built-in controllers (Deployment, ReplicaSet, etc.) or garbage collection. Simulate status updates that external controllers would normally perform.

**Framework and tooling:**
- Suites use Ginkgo/Gomega (`ginkgo.RunSpecs`, `It()`, `Context()`, `BeforeSuite()`)
- Use `pkgtest.NewEnvTest()` from `pkg/testing/` to set up suites - not raw `envtest.Environment{}`. The returned `*pkgtest.Client` wraps the K8s client, environment, and cleanup
- Check `pkg/testing/` for existing Gomega matchers (`BeOwnedBy`, `HaveCondition`, etc.) before writing new ones
- llmisvc has its own `fixture/` package (`pkg/controller/v1alpha2/llmisvc/fixture/`) with builders and envtest setup

**Isolation and cleanup:**
- Each test creates its own namespace
- Clean up resources with `defer` immediately after creation
- Use `retry.RetryOnConflict` when updating resources the controller is also reconciling

**Assertions:**
- Use `Eventually`/`Consistently`, never `time.Sleep`
- Simulate external controller behavior (status updates) with helper functions

## Development Workflow

1. **Analyze** - understand the task, identify affected controllers/types, search for reusable patterns
2. **Test first** - write the test before implementation code
3. **Implement** - minimal, focused changes following existing project patterns
4. **Verify** - run selective tests, then `make precommit` before committing

## Pull Requests

Use the template in `.github/PULL_REQUEST_TEMPLATE.md`. Fill in every section. Focus on **what** changed and **why** - avoid listing implementation details, files changed, or lines of code.

## Controller-Runtime Patterns

### Reconcile Loop

- Reconcile must be **idempotent** - same input run N times produces the same result
- Propagate `context.Context` via function arguments, avoid `context.Background()`
- Handle `NotFound` as success for deleted objects
- Use `Patch` with `MergeFrom` for updates to reduce conflicts
- Return errors for failures (controller-runtime handles backoff). `Requeue: true` only for async work in progress, `RequeueAfter` only for wall-clock delays
- Short-circuit when no-op - already-compliant objects should reconcile with no API calls

### Status and Conditions

- `spec` is user-owned (desired state), `status` is controller-owned (observed state) - never write both in a single API call
- Always include `observedGeneration` in conditions - without it, `Ready: True` may reflect a previous spec generation
- Use conditions (not phase fields) with CamelCase types and positive polarity: `Ready`, `Available`, etc.
- Define condition sets using `apis.NewLivingConditionSet(...)` in `*_lifecycle.go` files. Use typed `Mark*` helpers (`MarkXReady()`, `MarkXNotReady(reason, msg)`) for transitions - never manipulate condition slices directly. See `LLMInferenceService` lifecycle as the reference pattern
- Guard status writes with deep-equal checks - skip when nothing changed to avoid watch churn and infinite reconcile loops
- `reason` is CamelCase and part of the API contract. `lastTransitionTime` updates only when `status` (True/False/Unknown) changes, not on reason/message changes
- For composite conditions, surface the first failing sub-condition's Reason/Message in the parent so users don't have to inspect each one individually
- Use `ClearCondition()` to remove conditions that no longer apply rather than leaving stale values

### Watches and Caching

- Use predicates to drop irrelevant events early
- Prefer `Owns()` and targeted watches over broad `Watches()`
- Add field indexers to avoid expensive list+filter patterns
- Use `APIReader` only when cache staleness causes real correctness issues
- Cached client has no read-your-write consistency - writes hit the API server but are not instantly visible in cache

## ODH-specific conventions

- Reconcile idempotently; `NotFound` → success; guard status writes with deep-equal to avoid loops
- `spec` vs `status` in separate API calls; use `Mark*` helpers from `*_lifecycle.go` (see LLMISVC as reference), not raw condition slice edits
- Include `observedGeneration` in conditions; composite `Ready` should surface first failing sub-condition
- ODH networking/permissions changes → companion `*_odh.go` files listed in [architecture.md](docs/architecture.md)
