# Deploying the kserve-module to a Dev Cluster

This guide covers building, deploying, and configuring the kserve-module controller on CRC or any OpenShift cluster for development and testing.

## Prerequisites

- OpenShift cluster (CRC or ROSA) with `oc` logged in
- Docker or Podman
- A container registry you can push to (e.g. `quay.io/youruser`)
- cert-manager installed (required for webhook certificates)

## 1. Build and Push the kserve-module

```bash
export KO_DOCKER_REPO=quay.io/youruser
export TAG=dev

ENGINE=docker make -f Makefile.kserve-module.mk docker-push-kserve-module
```

## 2. Deploy the kserve-module

### Full deploy (CRDs + RBAC + controller)

This builds the kustomize overlay and applies everything via SSA — CRDs, RBAC, and the controller Deployment:

```bash
ENGINE=docker make -f Makefile.kserve-module.mk deploy-kserve-module
```

### Update only the controller image (if already deployed)

```bash
oc set image deployment/kserve-module-controller-manager \
  -n opendatahub \
  manager=quay.io/youruser/kserve-module-controller:dev
```

### Apply CRDs or RBAC independently

If you need to apply CRDs or RBAC separately (e.g. after adding new fields or RBAC markers without rebuilding the module image):

```bash
# CRD only
kubectl apply --server-side -f kserve-module/config/crd/components.platform.opendatahub.io_kserves.yaml

# RBAC only (after running make manifests-kserve-module)
kubectl apply --server-side -f kserve-module/config/rbac/role.yaml
```

### Verify the controller is running

```bash
oc get pods -n opendatahub -l app.kubernetes.io/name=kserve-module-controller-manager
oc logs -n opendatahub -l app.kubernetes.io/name=kserve-module-controller-manager -f
```

## 3. Override Component Images

The kserve-module resolves component images from embedded `params.env` files at build time. For dev testing, you can override any image via environment variables on the kserve-module-controller-manager deployment.

### Available image overrides

| Component | Env Var | Default source |
|-----------|---------|----------------|
| kserve-controller | `RELATED_IMAGE_ODH_KSERVE_CONTROLLER_IMAGE` | `config/overlays/odh/params.env` |
| llmisvc-controller | `RELATED_IMAGE_ODH_KSERVE_LLMISVC_CONTROLLER_IMAGE` | `config/overlays/odh/params.env` |
| kserve-agent | `RELATED_IMAGE_ODH_KSERVE_AGENT_IMAGE` | `config/overlays/odh/params.env` |
| kserve-router | `RELATED_IMAGE_ODH_KSERVE_ROUTER_IMAGE` | `config/overlays/odh/params.env` |
| storage-initializer | `RELATED_IMAGE_ODH_KSERVE_STORAGE_INITIALIZER_IMAGE` | `config/overlays/odh/params.env` |
| localmodel-controller | `RELATED_IMAGE_ODH_KSERVE_LOCALMODEL_CONTROLLER_IMAGE` | `config/overlays/odh-modelcache/params.env` |
| localmodelnode-agent | `RELATED_IMAGE_ODH_KSERVE_LOCALMODELNODE_AGENT_IMAGE` | `config/overlays/odh-modelcache/params.env` |
| odh-model-controller | `RELATED_IMAGE_ODH_MODEL_CONTROLLER_IMAGE` | hardcoded |
| wva-controller | `RELATED_IMAGE_ODH_WORKLOAD_VARIANT_AUTOSCALER_CONTROLLER_IMAGE` | `config/overlays/openshift/params.env` |

### Setting an override

```bash
oc set env deployment/kserve-module-controller-manager -n opendatahub \
  RELATED_IMAGE_ODH_KSERVE_CONTROLLER_IMAGE=quay.io/youruser/kserve-controller:dev
```

The module controller will restart automatically and reconcile with the new image on its next cycle.

### Removing an override (revert to default)

```bash
oc set env deployment/kserve-module-controller-manager -n opendatahub \
  RELATED_IMAGE_ODH_KSERVE_CONTROLLER_IMAGE-
```

Note the trailing `-` which removes the env var.

### Changing default images at build time

Edit the params.env files before building the kserve-module:

- `config/overlays/odh/params.env` — kserve, llmisvc, agent, router, storage-initializer
- `config/overlays/odh-modelcache/params.env` — localmodel-controller, localmodelnode-agent

## 4. Create the Kserve CR

The kserve-module is driven by a singleton `Kserve` custom resource named `default-kserve`.

### Minimal CR

```yaml
apiVersion: components.platform.opendatahub.io/v1alpha1
kind: Kserve
metadata:
  name: default-kserve
spec:
  managementState: Managed
```

### Full CR with all configurable features

```yaml
apiVersion: components.platform.opendatahub.io/v1alpha1
kind: Kserve
metadata:
  name: default-kserve
spec:
  # Overall management state. Set to Removed to undeploy all components.
  managementState: Managed

  # Controls whether Services use ClusterIP: None (Headless) or ClusterIP (Headed).
  # Headless is the default for RawDeployment mode.
  # Options: Headless, Headed
  rawDeploymentServiceConfig: Headless

  # NIM (NVIDIA Inference Microservice) integration
  nim:
    # Managed (default): deploys NIM account resources
    # Removed: skips NIM resources
    managementState: Managed
    # Set to true for air-gapped environments
    airGapped: false

  # WVA (Workload Variant Autoscaler)
  wva:
    # Removed (default): WVA not deployed
    # Managed: deploys WVA controller and resources
    managementState: Removed

  # ModelCache (local model caching on worker nodes)
  modelCache:
    # Removed (default): ModelCache not deployed
    # Managed: deploys localmodel controller, agent DaemonSet, PV/PVC, LMNG
    managementState: Managed
    # Required when Managed. Size of the host-path volume for cached models.
    cacheSize: 50Gi
    # Select nodes by name (mutually exclusive with nodeSelector)
    nodeNames:
      - worker-0
      - worker-1
    # OR select nodes by label (mutually exclusive with nodeNames)
    # nodeSelector:
    #   matchLabels:
    #     node-role.kubernetes.io/gpu: ""
```

### Apply the CR

```bash
oc apply -f kserve-cr.yaml
```

### Verify readiness

```bash
oc get kserve default-kserve -o jsonpath='{.status.phase}'
# Expected: Ready

oc get kserve default-kserve -o jsonpath='{.status.conditions}' | jq .
```

## 5. Troubleshooting

### CRD gets reverted after manual changes

The kserve-module SSA-applies all resources (including CRDs) every reconcile. Manual CRD edits will be overwritten. To test with a modified CRD:
1. Scale down the module: `oc scale deployment/kserve-module-controller-manager -n opendatahub --replicas=0`
2. Apply your CRD: `kubectl apply --server-side --force-conflicts -f config/crd/full/serving.kserve.io_inferenceservices.yaml`
3. Test, then scale back up when done

### Image not updating after env var change

The module sets the image on the target Deployment, but the pods may use a cached image if `imagePullPolicy: IfNotPresent`. Delete the pods to force a fresh pull:

```bash
oc delete pod -n opendatahub -l control-plane=kserve-controller-manager
```

### RBAC errors in controller logs

After adding new RBAC markers, regenerate and apply:

```bash
make manifests-kserve-module
kubectl apply --server-side -f kserve-module/config/rbac/role.yaml
oc delete pod -n opendatahub -l app.kubernetes.io/name=kserve-module-controller-manager
```

### Controller reconcile not triggering

Force a reconcile by annotating the Kserve CR:

```bash
kubectl annotate kserve default-kserve force-reconcile="$(date +%s)" --overwrite
```
