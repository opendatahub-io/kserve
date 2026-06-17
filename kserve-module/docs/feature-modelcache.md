# Feature: ModelCache

ModelCache pre-downloads models to node-local storage so InferenceServices can start without pulling from remote storage. It is managed as a sub-component of the kserve-module via the `Kserve` CR.

## Enable ModelCache

```bash
WORKER_NODE=$(oc get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[0].metadata.name}')

oc patch kserve default-kserve --type merge -p "{
  \"spec\": {
    \"modelCache\": {
      \"managementState\": \"Managed\",
      \"cacheSize\": \"5Gi\",
      \"nodeNames\": [\"$WORKER_NODE\"]
    }
  }
}"
```

### Using nodeSelector instead of nodeNames

```bash
oc patch kserve default-kserve --type merge -p '{
  "spec": {
    "modelCache": {
      "managementState": "Managed",
      "cacheSize": "50Gi",
      "nodeSelector": {
        "matchLabels": {
          "node-role.kubernetes.io/gpu": ""
        }
      }
    }
  }
}'
```

`nodeNames` and `nodeSelector` are mutually exclusive.

## Verify ModelCache Resources

When ModelCache is enabled, the kserve-module creates and manages the following resources:

```bash
# PersistentVolume — hostPath volume on labeled nodes
oc get pv kserve-localmodelnode-pv

# PersistentVolumeClaim — bound to the PV in the applications namespace
oc get pvc kserve-localmodelnode-pvc -n opendatahub

# LocalModelNodeGroup — tells the localmodel controller which nodes to use
oc get localmodelnodegroups.serving.kserve.io workers

# Node label — applied to each selected node
oc get node $WORKER_NODE -o jsonpath='{.metadata.labels.kserve/localmodel}'
# Expected: worker

# Namespace PSA — elevated to privileged for the agent DaemonSet
oc get ns opendatahub -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'
# Expected: privileged

# ConfigMap — localModel section enabled
oc get cm inferenceservice-config -n opendatahub -o jsonpath='{.data.localModel}' | jq .enabled
# Expected: true
```

## Disable ModelCache

```bash
oc patch kserve default-kserve --type merge -p '{"spec":{"modelCache":{"managementState":"Removed"}}}'
```

All ModelCache resources are cleaned up automatically:
- PV, PVC, and LocalModelNodeGroup are deleted
- Node labels (`kserve/localmodel`) are removed
- Namespace PSA is reverted to `baseline`
- ConfigMap `localModel.enabled` is set to `false`

## How It Works

### Architecture

The ModelCache feature deploys two controllers and a DaemonSet:

| Component | Role |
|-----------|------|
| `kserve-localmodel-controller-manager` | Orchestrates model downloads across node groups |
| `kserve-localmodelnode-agent` (DaemonSet) | Runs on labeled nodes, manages local model storage |
| `fix-permissions` (Job) | One-shot job to fix hostPath volume permissions on OpenShift |

### Storage

The PV uses a `hostPath` volume at `/var/lib/kserve/models` with `storageClassName: local-storage`. The `local-storage` StorageClass does not need to exist as a Kubernetes object — it is only used as a string label for static PV/PVC binding.

### SELinux (OpenShift)

On OpenShift, the DaemonSet agent pods are patched with the namespace's MCS level (`openshift.io/sa.scc.mcs` annotation) so their SELinux context matches the download jobs and fix-permissions jobs that share the same PVC. This is handled automatically during the kserve-module's post-render phase.

### Status Conditions

The `ModelCacheReady` condition on the Kserve CR reports:
- `Disabled` — ModelCache is not enabled
- `ResourcesReady` — all resources (PV, PVC, LMNG) exist and DaemonSet MCS matches
- `ResourcesNotReady` — one or more resources are missing
- `NamespaceMCSMissing` — the applications namespace is missing the `openshift.io/sa.scc.mcs` annotation
- `SELinuxMCSMismatch` — the DaemonSet MCS level doesn't match the namespace annotation

## Creating a LocalModelCache

Once ModelCache is enabled, create a `LocalModelCache` resource to pre-download a model:

```yaml
apiVersion: serving.kserve.io/v1alpha1
kind: LocalModelCache
metadata:
  name: qwen2-0.5b-instruct
spec:
  modelSize: 10Gi
  nodeGroups:
    - workers
  sourceModelUri: hf://Qwen/Qwen2-0.5B-Instruct
```

The localmodel controller will create download jobs on each node in the `workers` node group. Progress is reported in the `LocalModelCache` status:

```bash
oc get localmodelcache qwen2-0.5b-instruct -o jsonpath='{.status.nodeStatus}'
```

## Creating a LocalModelNamespaceCache

`LocalModelNamespaceCache` is the namespace-scoped variant of `LocalModelCache`. It pre-downloads a model for use by InferenceServices in a specific namespace, rather than cluster-wide.

```yaml
apiVersion: serving.kserve.io/v1alpha1
kind: LocalModelNamespaceCache
metadata:
  name: opt-125m
  namespace: modelcache-test
spec:
  modelSize: 2Gi
  nodeGroups:
    - workers
  sourceModelUri: hf://facebook/opt-125m
```

Check download status:

```bash
oc get localmodelnamespacecache opt-125m -n modelcache-test -o jsonpath='{.status}'  | jq .
```

Example output when download is complete:

```json
{
  "copies": {
    "available": 1,
    "total": 1
  },
  "nodeStatus": {
    "crc": "NodeDownloaded"
  }
}
```

### Difference from LocalModelCache

| | LocalModelCache | LocalModelNamespaceCache |
|---|---|---|
| Scope | Cluster-wide | Single namespace |
| Created by | Cluster admin | Namespace user |
| PV/PVC | Shared across namespaces | Scoped to the namespace |
| Use case | Shared models across teams | Team-specific models |
