# llm-d Technology Preview retirement

llm-d provisioning is retired in this release. Ordinary `InferenceService`
serving is not affected.

- New `LLMInferenceService` and `LLMInferenceServiceConfig` objects are rejected.
- Existing objects remain deletable, but spec changes are rejected. Metadata,
  finalizer, and status updates remain allowed.
- Existing workloads are unmanaged: their resources are not reconciled or
  repaired, and readiness is no longer monitored. Services report
  `Unknown / Unmanaged` rather than retaining stale readiness.
- Kubernetes continues to manage existing workload Deployments and pods. Keep
  their existing container images available until the workloads are removed.

## Before upgrading to 3.x

Customers must delete **all existing `LLMInferenceService` objects in every
namespace** before upgrading to 3.x. Plan workload migration before deleting
these services; deleting them removes their owned serving resources.

Confirm no services remain:

```sh
oc get llminferenceservices.serving.kserve.io --all-namespaces
```

Wait for deletion and finalizer cleanup to finish before proceeding. Do not
assume that unmanaged services can be carried forward into a 3.x installation.
