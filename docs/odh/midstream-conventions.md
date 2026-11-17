# Midstream conventions

`opendatahub-io/kserve` is upstream [kserve/kserve](https://github.com/kserve/kserve) plus what Open Data Hub needs on OpenShift. Every line we change in a file upstream owns is a line the next sync has to fight over. For a long time that was a lot of lines - syncs took days, and reviewers had to tell ODH logic from upstream logic by squinting at `if` statements.

These conventions exist so that a sync is mostly a merge, and the diff to upstream is something you can actually read. This page is the map. The detailed rules live in [`.rules/`](../../.rules), which CodeRabbit also enforces on every PR.

## The one rule

Don't edit files upstream owns. If ODH needs different behaviour, upstream gets a seam and the ODH part lives next to it, in a file upstream never compiles.

A file is upstream-owned if it exists in `kserve/kserve`:

```sh
git cat-file -e upstream/master:pkg/controller/v1beta1/inferenceservice/controller.go && echo upstream-owned
```

Everything below is that rule applied to Go code, RBAC, manifests, builds and tests.

## Where things go

| You need to... | Put it in | Rule |
|---|---|---|
| Change what upstream code does on OpenShift | A hook call upstream, a no-op `*_default.go` (`//go:build !distro`) and the implementation in `*_odh.go` (`//go:build distro`) | [build-tags](../../.rules/build-tags.md) |
| Add ODH-only code nothing upstream calls | `*_odh.go`, no default needed | [build-tags](../../.rules/build-tags.md) |
| Grant permissions only ODH needs | Markers in `<controller>/distro/controller_rbac_odh.go`, generated with `make manifests-distro` | [rbac-isolation](../../.rules/rbac-isolation.md) |
| Change a manifest | A patch in `config/overlays/odh/`, or kserve-module when the value depends on the cluster | [kustomize-hygiene](../../.rules/kustomize-hygiene.md) |
| Add a make target or variable | `Makefile.overrides.mk` | [makefile-split](../../.rules/makefile-split.md) |
| Build a new Go binary | `GOTAGS` passed from the Makefile through the Dockerfile to `go build -tags` | [distro-builds](../../.rules/distro-builds.md) |
| Depend on an OpenShift-only Go module | The distro `require` block at the end of `go.mod`, imported only from `distro`-tagged files | - |
| Write an e2e test that runs on OpenShift | No hardcoded `runAsUser`, TLS-aware commands | [e2e-openshift-compat](../../.rules/e2e-openshift-compat.md) |
| Document something ODH-specific | `docs/odh/`, never upstream's `README.md`, `CONTRIBUTING.md` or `docs/` pages | - |

The suffix is `_odh`. You'll still find `_ocp` in old commit messages - it's the same thing, renamed.

## A hook pair, end to end

The InferenceService controller grants an image-volume SCC to workloads that mount OCI models. Upstream has no SCCs, so the controller calls a hook once the components are reconciled ([kserve/kserve#6313](https://github.com/kserve/kserve/pull/6313)):

```go
// controller.go - upstream-owned
if err := r.postReconcilePlatform(ctx, isvc, isvcConfigMap); err != nil {
	return ctrl.Result{}, err
}
```

Upstream ships the no-op:

```go
//go:build !distro

// platform_reconcile_default.go
func (r *InferenceServiceReconciler) postReconcilePlatform(_ context.Context, _ *v1beta1.InferenceService, _ *corev1.ConfigMap) error {
	return nil
}
```

ODH ships the real thing:

```go
//go:build distro

// workload_permissions_odh.go
func (r *InferenceServiceReconciler) postReconcilePlatform(ctx context.Context, isvc *v1beta1.InferenceService, isvcConfigMap *corev1.ConfigMap) error {
	if err := r.reconcileWorkloadPlatformPermissions(ctx, isvc, isvcConfigMap); err != nil {
		return fmt.Errorf("fails to reconcile workload platform permissions: %w", err)
	}
	...
}
```

The LLMInferenceService controller was the first to do this end to end (`controller_setup_{default,odh}.go` in `pkg/controller/v1alpha2/llmisvc`), and it's still the best place to see the whole pattern in one controller.

What we learned writing these:

- **Name hooks after where they run, not what ODH does with them.** `postReconcilePlatform`, not `reconcileWorkloadPlatformPermissions`. Upstream has to live with the name, and the next distro will use it for something else.
- **Make it a receiver method when it needs the reconciler's client or state.** A package function is fine when everything comes in as arguments (`resolvePlatformIngressReconciler` gets its params).
- **Return errors unwrapped at the call site.** The implementation owns its error text, so the upstream line never changes when ODH rewords a message.
- **Pass resolved values through the context.** Some hooks only see component metadata, not the whole InferenceService. `preReconcilePlatform` returns a derived context, and `customizeDeployments` reads what it needs from it.
- **Don't trust labels to tell owners apart.** They're user-controlled. If two controllers share a code path, have the caller say who it is.

## Upstream first

A hook needs an upstream PR, and the order matters:

1. **Open the hook PR in `kserve/kserve`.** Small, no ODH behaviour, a doc comment saying exactly when the hook runs and what it may do.
2. **Open the midstream PR with the hook lines byte-identical** - cherry-pick the upstream commit - plus the ODH implementation, and link the upstream PR.
3. **Keep the two in step.** If review upstream changes the shape, re-cut the midstream PR before it merges, not after. Once upstream merges, the next sync is a no-op for those lines.

If upstream says no, try a smaller, more generic seam first - most rejections are about naming or scope, not the idea. If it really has to be a midstream-only change to an upstream file, keep it to one line, make the reason obvious in the code, and track it in JIRA so it gets another go later.

## Why build tags

We looked at the alternatives. Each one moves the problem rather than removing it:

- **Runtime feature flags.** ODH code ships in upstream binaries, OpenShift APIs land in upstream's `go.mod`, and the `if odh { ... }` branches are exactly the diff we're trying to get rid of. Upstream would never take it either.
- **A long-lived fork branch or a patch queue.** Every sync rebases every patch, and each patch is a conflict waiting to happen. We had a carry-over patch plan (RHOAIENG-56272) and dropped it once hooks covered the cases.
- **A separate controller.** It works until two controllers own the same object. odh-model-controller and kserve both touching InferenceService Routes is the cautionary tale ([#2062](https://github.com/opendatahub-io/kserve/pull/2062) ends it).

Build tags keep upstream builds ODH-free by construction, and the compiler checks the boundary: a missing `_odh.go` implementation is a build failure, not a silent behaviour change. The price is two builds to keep green, which is cheap compared to a week-long sync.

## Testing both builds

- **Build and vet both.** `go build ./...` and `go build -tags distro ./...` - CI runs `go test -vet=all -c` for both ([distro-build-check](../../.github/workflows/distro-build-check.yml)).
- **Upstream's specs are the parity check.** They run in the non-distro build, so a midstream change that leaks into upstream behaviour shows up as an upstream spec failing. ODH-flavoured specs live in `*_odh_test.go` behind `//go:build distro`.
- **Lint runs per build.** A `//nolint` that one build needs can be unused in the other. If only distro code would trigger a lint difference in an upstream file, change the distro code, not the upstream file.
- **Run `make precommit` before pushing**, and check `git status` afterwards - it regenerates and fixes files in place, and CI fails on a dirty tree.

## Syncing

- **`_default.go` add/add conflicts:** once upstream merges a hook, its `_default.go` arrives as an add/add conflict with ours. Take upstream's version - midstream only carried it until then.
- **Release branches:** see [fetch-upstream-release.md](fetch-upstream-release.md).
- **Automation:** AI-assisted sync is tracked in RHOAIENG-58821.

## When nothing fits

Ask before you inline. A midstream change to an upstream file is never "just this once" - it's the line someone resolves by hand at every sync until it's moved. If you're about to edit an upstream file, that's the moment to look for the seam, or to open the upstream PR that adds one.

Future you, doing the next sync, says thanks.
