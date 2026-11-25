# Architecture (Agent Reference)

Controller reconciliation flows and the distro build-tag pattern for the ODH midstream fork.

**Shared config:** `inferenceservice-config` ConfigMap in the `kserve` namespace drives deployment modes, ingress, local model settings, router image, and feature flags.

## Binaries

| Binary | Entry | Controllers |
|--------|-------|-------------|
| KServe manager | `cmd/manager/` | InferenceService, InferenceGraph, TrainedModel |
| LLMISVC | `cmd/llmisvc/` | LLMInferenceService |
| Local model | `cmd/localmodel/` | LocalModelCache, LocalModelNamespaceCache, LocalModelNode |

APIs: `pkg/apis/serving/`. Controllers: `pkg/controller/{v1alpha1,v1alpha2,v1beta1}/`.

## Distro build tag pattern

Upstream-owned files must stay ODH-free. Platform code compiles only with `-tags distro`:

| File | Tag | Role |
|------|-----|------|
| `*_odh.go` | `distro` | ODH/OpenShift implementation |
| `*_default.go` | `!distro` | No-op stub for upstream builds |
| `distro/controller_rbac_odh.go` | none | ODH RBAC markers for `make manifests-distro` |

Hook pattern: upstream calls e.g. `extendControllerSetup()` → `controller_setup_{default,odh}.go` in llmisvc. Additive-only files (`constants_odh.go`, `register_odh.go`) need no `_default.go` if upstream never calls them.

`Makefile.overrides.mk` sets `GOTAGS=distro`. Tag must propagate Makefile → Docker `GOTAGS` build-arg → `go build -tags` (missing tag silently drops ODH code). CI: `.github/workflows/distro-build-check.yml`.

The reasoning, the upstream-first workflow and the full placement table are in [`docs/odh/midstream-conventions.md`](odh/midstream-conventions.md).

## InferenceService (ISVC)

`pkg/controller/v1beta1/inferenceservice/` · `InferenceServiceReconciler.Reconcile()`

1. Load `inferenceservice-config` → resolve deployment mode (Standard / Knative / ModelMesh)
2. Finalizer on create; cleanup + remove on delete
3. ModelMesh without transformer → skip predictor reconcile, update status only
4. Reconcile components: Predictor (skipped in ModelMesh), Transformer, Explainer — each creates Deployment or Knative Service
5. Ingress via `reconcilers.NewReconcilerFactory()` (Istio VS, Ingress, HTTPRoute, OpenShift Route)
6. `modelconfig` ConfigMap reconcile → status update

**ODH:** `reconcilers/service/service_reconciler_odh.go`, `reconcilers/ingress/annotation_filter_odh.go`, `components/annotation_filter_odh.go`, `pkg/apis/serving/v1beta1/configmap_odh.go`, `distro/controller_rbac_odh.go`

## LLMInferenceService (LLMISVC)

`pkg/controller/v1alpha2/llmisvc/` · `LLMISVCReconciler.Reconcile()`

1. Finalizer; `finalize()` cleans platform resources (ODH: monitoring) and scheduler SA on delete
2. CA bundle ConfigMap reconcile
3. `reconcileBaseRefs()` — merge `LLMInferenceServiceConfig` refs into effective spec (failure sets condition, short-circuits)
4. `reconcileWorkload()` — platform workload preparation (ODH: confidential namespace helpers and customer-provided block PVC validation), TLS certs, platform permissions, then all topology types every pass (single Deployment, LeaderWorkerSet multi-node, prefill/decode disaggregated, HPA/KEDA/VariantAutoscaling)
5. `reconcileRouter()` — HTTPRoutes, InferencePool (v1/v1alpha2), scheduler; `ensureGatewayPreconditions()` marks status without requeue on missing CRDs
6. `reconcilePlatformResources()` (ODH: monitoring; per-LLMISVC NetworkPolicies — see below) → `observeWorkloadStatus()` → status (composite `Ready` from workload + router sub-conditions)

### NetworkPolicies (ODH)

#### Integration into the controller

NetworkPolicies run at step 6 above, after workload and router reconciliation and before
`observeWorkloadStatus()`. Configuration, base-reference, workload, or router failures return before
this stage. Policies use the effective spec already merged by `reconcileBaseRefs()`.

The distro hook calls through the monitoring code, but reconciles policies **before** Prometheus resources:

```text
reconcilePlatformResources()             platform_resources_odh.go
  → reconcileMonitoringResources()       monitoring_odh.go
    → reconcileNetworkPolicies()         monitoring_network_policy_odh.go
      → reconcileLLMISVCNetworkPolicies() network_policy_odh.go
```

Inside `reconcileLLMISVCNetworkPolicies()`, ingress runs first, then egress runs even if ingress failed;
errors are combined. This independence applies within the policy stage, not to earlier controller
stages. A policy error propagates through the platform hook and prevents the later monitoring and
workload-observation steps; the outer `Reconcile()` still attempts its normal status update and
returns the error for retry.

#### Policy decisions and Pod selection

Ingress and egress are separate policy sets:

- **Ingress:** `llmisvc.featureGates.strictNetworkPolicy` in `inferenceservice-config` (default **off**)
  selects the mode. Default creates allow-all ingress policies
  (`<name>-networkpolicy-default-<component>`); strict creates workload/scheduler/tokenizer ingress
  policies with Gateway, scheduler, monitoring, and workload-peer sources
  (`<name>-networkpolicy-strict-<component>`). Both sets are ingress-only.
- **Egress:** `reconcileEgressNetworkPolicies()` in `egress_network_policy_odh.go` makes the same
  decision in either ingress mode. Normally it creates an egress-only allow-all policy per stored
  component (`<name>-networkpolicy-egress-<component>`, `egress: [{}]`), providing an explicit grant even
  under namespace default-deny egress. Only effective `spec.tracing` plus
  `serving.kserve.io/enable-tracing-egress-network-policy: "true"` selects tracing restrictions:
  reconcile `<name>-otlp-egress`, then remove the owned general allow-all grants. Tracing without this
  exact opt-in keeps allow-all egress.

Ingress and general egress use `networkPolicySelectors()` in `network_policy_selectors_odh.go`:
complete stored Deployment selectors and LWS/DisaggregatedSet membership labels, including stored
components at zero replicas or left during topology transitions. No policy-specific Pod labels are
added. Ingress mode toggles change ingress policies, not Pod templates or egress policy names.

Tracing retains its existing application-label selector and DNS, HTTPS/API, same-namespace, and OTLP
rules (`tracing_network_policy_odh.go`); its selector need not match the stored-component inventory.
Permissions from selecting policies are additive. Overlapping selectors across services remain a
deferred limitation (see `docs/odh/llmisvc-networkpolicy-design.md`).

#### Standalone PVC evictors

Standalone `pvc.ref` evictors have a separate Deployment-owned deny-all policy,
not an entry in the seven-component inventory. The generic
`postReconcilePVCEvictors()` hook runs immediately after shared-evictor
reconciliation (even on partial failure), before router/monitoring reconciliation.
Its distro implementation copies validated stored selectors, sets both policy
types with no rules, and watches policies for repair. No Pod labels/templates change.

The Deployment UID owns each policy because services can share an evictor.
Existing evictor cleanup deletes unreferenced Deployments; Kubernetes GC removes
their policies. Per-service policy finalization never prunes shared evictor policies.
Default/strict and tracing changes do not alter this baseline. The policy kill
switch suppresses creation/repair, not retroactive deletion; monitoring disablement
does not suppress it. See [standalone evictor policies](odh/standalone-evictor-networkpolicies.md)
for custom-network exceptions, selector constraints, and enforcement limits.

#### Replacement, failures, and migration

- Establish the complete desired ingress set before pruning stale ingress names.
- When opting out of tracing, establish **all** desired allow-all egress grants before deleting
  tracing or stale grants. When opting in, establish tracing before removing general grants.
  Establishment failures retain prior policies and retry without creating fallback policies;
  transitions are not atomic.
- Remove retired `<name>-networkpolicy-egress` only after its egress replacement is established.
  Remove the old combined `<name>-networkpolicy-allow-all` and RHAI-4088 `<name>-prometheus-scraping`
  only after both ingress and egress reconciliation, including pruning, succeed. Cleanup errors
  propagate for retry, so prior broad grants can remain until convergence.

#### Finalization and disable switches

Deletion follows `finalize()` → `finalizePlatformResources()` → `cleanupMonitoringResources()` →
`cleanupLLMISVCNetworkPolicies()`. Policy cleanup precedes shared-monitoring cleanup and covers a
bounded set of owned names: both ingress modes, all seven component egress names, tracing, and retired
policies, regardless of current topology. Missing policies are success; foreign policies are skipped;
API failures retain the service finalizer for retry.

`LLMISVC_MONITORING_DISABLED=true` does not skip policy reconciliation or finalization.
`LLMISVC_NETWORK_POLICY_DISABLED=true` (set by the xKS overlay) skips normal policy reconciliation;
finalization still attempts owned-policy cleanup.

**ODH:** `controller_setup_odh.go`, `platform_resources_odh.go`, `monitoring{,_network_policy}_odh.go`, `workload_{tls_cert,tls_rotation,permissions,confidential,modelexpress}_odh.go`, `confidential_{config,helpers,watches}_odh.go`, `router_{preconditions,platform_networking,discovery_additional}_odh.go`, `distro/controller_rbac_odh.go`

## InferenceGraph

`pkg/controller/v1alpha1/inferencegraph/` · `InferenceGraphReconciler.Reconcile()`

1. Finalizer; auth SA cleanup on delete
2. Resolve each step's `ServiceURL` from referenced ISVC via `GetPredictorEndpoint()` (not-ready → requeue)
3. **Standard:** auth resources if enabled → router Deployment/Service/HPA → OpenShift Route if CRD present → `PropagateRawStatus`
4. **Knative:** router Knative Service → propagate ksvc conditions/URL to graph status
5. Force-stop annotation handling → status update

Router image/resources from `router` key in `inferenceservice-config`.

**ODH:** `distro/controller_rbac_odh.go`

## ModelCache (LocalModel)

Pre-downloads models to node-local storage for ISVCs/LLMISVCs labeled `serving.kserve.io/local-model: <modelName>`.

| Controller | Package | Role |
|------------|---------|------|
| LocalModelCache | `localmodel/reconcilers/` | Cluster-scoped; orchestrates nodes + PV/PVC |
| LocalModelNamespaceCache | same | Namespace-scoped variant |
| LocalModelNode | `localmodelnode/` | DaemonSet agent per node |

**LocalModelCache reconcile** (`localmodelcache_reconciler.go`):

1. Deleting → `DeleteModelFromNodes()`; else add finalizer
2. `ReconcileLocalModelNode()` — add model to node group `LocalModelNode` specs
3. Per node group: download PV/PVC in job namespace
4. `ReconcileForIsvcs()` — consumer PV/PVC per labeled ISVC/LLMISVC

**LocalModelNode** (DaemonSet, `NODE_NAME` env): for each model — check hash-based folder under `models/`; if missing launch/monitor storage-initializer Job; update `Status.ModelStatus`; prune orphaned folders/jobs on spec change.

**Watches re-enqueue cache CRs on:** ISVC/LLMISVC local-model label change, matching Node ready, LocalModelNode status change, owned PVC change.

### Download egress NetworkPolicies (ODH)

Two egress-only policies keep download Jobs working in namespaces with default-deny
egress (details: [`docs/odh/modelcache-download-networkpolicies.md`](odh/modelcache-download-networkpolicies.md)):

- **Static per-node:** `kserve-localmodel-download` ships in the odh-modelcache overlay
  and selects `serving.kserve.io/localmodel-download: node`, stamped onto download Job
  pod templates by the `enhanceDownloadJob` hook. The manifest owns its lifecycle: no
  controller RBAC, watch, or cleanup.
- **Cache-owned import:** one `<cache-name>-import-egress` per shared-PVC-mode (`pvcRef`)
  `LocalModelNamespaceCache` that reaches import reconciliation — after PVC preflight and
  destination checks; `nodeGroups`-mode caches never get one — in that CR's namespace,
  owned via `SetControllerReference`. Created before the import
  Job and kept across retries/replacement/completion; garbage-collected with the cache
  (the existing finalizer does not delete it). Watched as an owned resource for
  delete/drift recovery; only policies controlled by the exact cache UID are reconciled
  — a foreign or stale-UID policy blocks the import instead of being adopted.

Both use one empty `egress: [{}]` rule (explicit allow-all: download destinations are
deployment-specific) and add no ingress rules. Labels go only onto newly built Jobs;
existing Jobs are never relabeled. Selection is name-based (role plus cache-name
labels); no UID-based selector isolation is claimed.

**ODH:** `localmodelnode/{platform,file_utils}_odh.go`, `localmodel/reconcilers/sharedpvc_platform_{default,odh}.go`, `localmodel/distro/controller_rbac_odh.go`, `localmodelnode/distro/controller_rbac_odh.go`
