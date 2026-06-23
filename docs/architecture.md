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

## InferenceService (ISVC)

`pkg/controller/v1beta1/inferenceservice/` · `InferenceServiceReconciler.Reconcile()`

1. Load `inferenceservice-config` → resolve deployment mode (Standard / Knative / ModelMesh)
2. Finalizer on create; cleanup + remove on delete
3. ModelMesh without transformer → skip predictor reconcile, update status only
4. Reconcile components: Predictor (skipped in ModelMesh), Transformer, Explainer — each creates Deployment or Knative Service
5. Ingress via `reconcilers.NewReconcilerFactory()` (Istio VS, Ingress, HTTPRoute, OpenShift Route)
6. `modelconfig` ConfigMap reconcile → status update

**ODH:** `reconcilers/service/service_reconciler_odh.go`, `reconcilers/ingress/annotation_filter_odh.go`, `components/annotation_filter_odh.go`, `pkg/apis/serving/v1beta1/configmap_odh.go`

## LLMInferenceService (LLMISVC)

`pkg/controller/v1alpha2/llmisvc/` · `LLMISVCReconciler.Reconcile()`

1. Finalizer; `finalize()` cleans scheduler SA and monitoring on delete
2. CA bundle ConfigMap reconcile
3. `reconcileBaseRefs()` — merge `LLMInferenceServiceConfig` refs into effective spec (failure sets condition, short-circuits)
4. `reconcileWorkload()` — TLS certs, platform permissions, then all topology types every pass (single Deployment, LeaderWorkerSet multi-node, prefill/decode disaggregated, HPA/KEDA/VariantAutoscaling)
5. `reconcileRouter()` — HTTPRoutes, InferencePool (v1/v1alpha2), scheduler; `ensureGatewayPreconditions()` marks status without requeue on missing CRDs
6. Monitoring resources → `observeWorkloadStatus()` → status (composite `Ready` from workload + router sub-conditions)

**ODH:** `controller_setup_odh.go`, `workload_{tls_cert,permissions}_odh.go`, `router_{preconditions,platform_networking,discovery_additional}_odh.go`, `distro/controller_rbac_odh.go`

## InferenceGraph

`pkg/controller/v1alpha1/inferencegraph/` · `InferenceGraphReconciler.Reconcile()`

1. Finalizer; auth SA cleanup on delete
2. Resolve each step's `ServiceURL` from referenced ISVC via `GetPredictorEndpoint()` (not-ready → requeue)
3. **Standard:** auth resources if enabled → router Deployment/Service/HPA → OpenShift Route if CRD present → `PropagateRawStatus`
4. **Knative:** router Knative Service → propagate ksvc conditions/URL to graph status
5. Force-stop annotation handling → status update

Router image/resources from `router` key in `inferenceservice-config`.

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

**ODH:** `localmodelnode/{platform,file_utils}_odh.go`, `localmodel/distro/controller_rbac_odh.go`, `localmodelnode/distro/controller_rbac_odh.go`
