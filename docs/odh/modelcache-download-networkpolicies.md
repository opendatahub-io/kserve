# ModelCache download egress NetworkPolicies

**Status:** Implemented. Two egress-only NetworkPolicies keep ModelCache download Jobs
working in namespaces that enforce default-deny egress, part of
[RHOAIENG-93716](https://redhat.atlassian.net/browse/RHOAIENG-93716). Only downloads are
covered: evictors, tracing, and serving-workload policy changes are out of scope. This
document describes rules and lifecycle; it records no cluster mutation or testing (see
[Cluster verification](#cluster-verification) for the manual checks that were not run).

## Summary

ModelCache downloads run in Jobs whose destinations — model storage, registries,
identity providers, DNS — are deployment-specific, so both policies grant an explicit
allow-all egress rule instead of pinning endpoints:

| Policy | Name | Namespace | Selected pods | Lifecycle owner |
| --- | --- | --- | --- | --- |
| Per-node download | `kserve-localmodel-download` | `model-serving-cache` (the module's Job namespace) | `serving.kserve.io/localmodel-download: node` | the odh-modelcache overlay manifest |
| Shared-PVC import | `<cache-name>-import-egress` | the `LocalModelNamespaceCache` CR's namespace | `serving.kserve.io/localmodel-download: shared-pvc` **and** `internal.serving.kserve.io/localmodel: <cache-name>` | the cache CR (controller reference) |

Both are **egress-only**: they add no ingress rules and do not change ingress isolation.
The selected pods keep their namespace's default ingress behavior, and no other pod is
affected. The role label is what distinguishes import pods from serving pods that carry
the same cache-name label, so neither identity nor role alone is sufficient to match the
import selector.

## Static per-node policy (overlay-managed)

[`config/overlays/odh-modelcache/network-policies.yaml`](../../config/overlays/odh-modelcache/network-policies.yaml)
ships `kserve-localmodel-download` with `policyTypes: [Egress]` and one empty
`egress: [{}]` rule. The manifest owns this policy's lifecycle: no controller creates,
watches, or cleans it, and no owner reference is set. There are no dynamic per-node
policies.

The localmodelnode agent stamps `serving.kserve.io/localmodel-download: node` onto the
download Job pod template through the distro `enhanceDownloadJob` hook
([`pkg/controller/v1alpha1/localmodelnode/platform_odh.go`](../../pkg/controller/v1alpha1/localmodelnode/platform_odh.go)),
merging into the template so caller-set labels and annotations survive. The label
constants live in
[`pkg/constants/constants_odh.go`](../../pkg/constants/constants_odh.go);
the module render test
(`kserve-module/pkg/kservemodule/modelcache_manifest_test.go`) pins the manifest
selector literals, and the root-package tests
([`pkg/controller/v1alpha1/localmodelnode/platform_odh_test.go`](../../pkg/controller/v1alpha1/localmodelnode/platform_odh_test.go)
and
[`pkg/controller/v1alpha1/localmodel/reconcilers/sharedpvc_platform_odh_test.go`](../../pkg/controller/v1alpha1/localmodel/reconcilers/sharedpvc_platform_odh_test.go))
pin the Go constants to the same literals. No other workload in the namespace carries
the label, so agent, permission-fix, import and serving pods are not selected.

### Namespace wiring and custom Job namespaces

The KServe module renders the overlay into `model-serving-cache` and sets
`localModel.jobNamespace` to the same value
([`kserve-module/pkg/kservemodule/configmap.go`](../../kserve-module/pkg/kservemodule/configmap.go)),
so the node agent creates its download Jobs exactly where the static policy applies.

An out-of-band installation that points `localModel.jobNamespace` at a different
namespace must also install `kserve-localmodel-download` there: the policy is static and
neither the controllers nor the module chase configuration changes dynamically. The
cache-owned import policy needs no such step — the controller creates it in the cache
CR's own namespace.

## Cache-owned import policy (controller-managed)

For every shared-PVC-mode `LocalModelNamespaceCache` (`spec.pvcRef` set) that reaches
import reconciliation, the manager reconciles one egress-only allow-all NetworkPolicy
named `<cache-name>-import-egress` in that CR's namespace, owned by the CR through
`controllerutil.SetControllerReference`
([`pkg/controller/v1alpha1/localmodel/reconcilers/sharedpvc_platform_odh.go`](../../pkg/controller/v1alpha1/localmodel/reconcilers/sharedpvc_platform_odh.go)):

- **Who gets one:** the policy is ensured inside `getOrCreateImportJob`, which the
  reconcile path reaches only after the finalizer is set, the referenced PVC is found
  and not terminating, PVC preflight (volume mode, access mode, capacity) passes, and
  the destination-ownership check passes. A cache whose preflight fails may never
  reach the ensure step. `nodeGroups`-mode caches never get a cache-owned policy: their
  per-node download Jobs run in the shared Job namespace and are covered by the static
  overlay policy.

- **Ordering:** ensured before any import Job is created. A real API failure is
  returned for a retry and prevents a new Job from starting without the grant.
- **Retention:** kept across Job retries, replacement, completion, and failure.
- **Teardown:** garbage-collected with the cache CR. The existing finalizer does not
  explicitly delete it, and the generated LocalModel RBAC grants no networkpolicy
  patch or delete.
- **Self-healing:** watched as an owned resource, so deleting or drifting the policy
  re-enqueues the cache and the next reconcile restores it. Reconciliation of an
  existing policy is idempotent and only rewrites the spec and the controller owner
  reference.
- **Ownership:** only a policy controlled by the exact cache object (same UID) is
  reconciled. A foreign policy, or one left by a previous incarnation of the cache
  (same name, different UID, still awaiting garbage collection), is never adopted and
  its owner reference is never rewritten: the reconcile fails and blocks the import
  until the name is free.

## Additive allow-all and namespace-local scope

- Kubernetes NetworkPolicy permissions are additive across every policy selecting a
  pod. The empty `egress: [{}]` rule is an explicit allow-all grant: every destination,
  protocol, and port for the selected pods. Under namespace default-deny egress this is
  what keeps downloads working — without a selecting grant the pods would have no
  egress at all. The grant cannot revoke anything either: if other policies also
  select the same pods, the union applies and these policies do not narrow it.
- NetworkPolicy is namespaced. Each policy applies only in the namespace it is created
  in: the static policy covers `model-serving-cache`, the import policy covers only the
  cache CR's namespace. Neither grants anything to pods in other namespaces.

## Name and label constraints

- The import selector reuses the cache name as a **label value**
  (`internal.serving.kserve.io/localmodel: <cache-name>`), so the cache name must fit
  the 63-character label-value limit. This change assumes it does: the literal name is
  used, with no shortening, hashing, validation restriction, or long-name support.
  Cache names longer than 63 characters are outside this change's scope; the generic
  `importJobName` behavior and its tests are unchanged.
- The NetworkPolicy **object name** `<cache-name>-import-egress` also uses the literal
  cache name, but object names are not bound by the label-value limit.

## Upgrade behavior: new Jobs only

The labels are stamped on the Job pod template when the Job is built. Existing Jobs
and Pods are never relabeled, immutable Job templates are not rewritten, and imports
are never recreated just to gain policy coverage:

- After an upgrade, an already-running or retained unlabeled import Job stays
  unlabeled; its retries remain unselected by the new policy.
- A Job replacement created later through the controller's normal path picks up the
  new template labels and is covered. Deleting a retained Job is **not** an automatic
  upgrade remediation: the existing re-import gates still apply — no replacement is
  created while consumers still reference the cache (or pods still mount the claim).
- New caches and new imports are covered from creation.

## Selector reuse across CR recreation

Selection is by label values — the download role plus the cache name — not by object
UID. A recreated cache CR with the same name reuses the same selector, and its import
pods are matched by name. **No UID-based selector-isolation guarantee is claimed** for
the pods themselves; the UID check applies only to the policy object's ownership, as
described above.

## RBAC

Only the LocalModel manager role grants networkpolicy access: `networking.k8s.io`
`networkpolicies` `get`/`list`/`watch`/`create`/`update`, generated into
`kserve-localmodel-distro-role` from
[`pkg/controller/v1alpha1/localmodel/distro/controller_rbac_odh.go`](../../pkg/controller/v1alpha1/localmodel/distro/controller_rbac_odh.go).
No `patch` or `delete` — teardown is owner-reference garbage collection. The node agent
and permission-fix identities gain no new privileges; the static overlay policy needs
no controller RBAC at all.

## Cluster verification

No cluster was mutated or tested for this change. To verify on a representative
OpenShift cluster — with namespace default-deny egress where a completion or a denial
is claimed, and accounting for every matching policy, since egress permissions are
additive and a denied path only proves isolation when no other selecting policy grants
it:

1. **Static policy present:** `kubectl get networkpolicy kserve-localmodel-download -n
   model-serving-cache` shows `policyTypes: [Egress]`, one empty egress rule, and the
   single-label node selector. The manifest owns its lifecycle: it survives controller
   restarts and cache churn, and is removed only with the overlay install (module
   uninstall or a manifest change), never by a controller.
2. **Per-node download under default-deny:** with default-deny egress in
   `model-serving-cache`, trigger a per-node model download. The Job pod template
   carries `serving.kserve.io/localmodel-download: node` and the model actually
   downloads.
3. **Shared-PVC import under default-deny:** create a shared-PVC-mode
   `LocalModelNamespaceCache` (spec.pvcRef set; spec.nodeGroups unset).
   `<cache-name>-import-egress` appears in the cache's namespace before its import Job,
   with the two-label selector and an owner reference to the cache, and the model
   actually imports onto the claim.
4. **Unrelated pods stay denied:** a pod in the same namespace that carries neither
   download label — and is selected by no other granting policy — still cannot egress.
5. **Retries and replacement:** pod retries of a newly labeled Job are selected (the
   labels live on the pod template), and a replacement Job created through the
   controller's normal path is covered from creation. Retries of a pre-upgrade
   unlabeled Job remain unselected.
6. **Per-cache teardown:** delete one cache CR; only its `<cache-name>-import-egress` is
   garbage-collected. Other caches' policies in the same namespace remain, and the
   static `kserve-localmodel-download` is untouched. A directly deleted cache-owned
   policy is recreated by the next reconcile through the owned-resource watch.
