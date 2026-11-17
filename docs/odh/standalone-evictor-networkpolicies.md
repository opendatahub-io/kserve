# Standalone PVC evictor NetworkPolicies

ODH KServe creates one deny-all NetworkPolicy for each stored standalone
KV-cache evictor Deployment (`pvc.ref`). The evictor's basic job is to reclaim
space by deleting cold cache files on its mounted PVC. This baseline grants no
application network access.

## Selection and traffic

The policy name is derived with `kmeta.ChildName(deployment.Name,
"-networkpolicy-deny-all")`. It copies the complete stored Deployment selector,
including additional match labels and expressions. Current selectors use these
existing labels:

- `serving.kserve.io/pvc-evictor: "true"`
- `serving.kserve.io/pvc-evictor-key: <PVC/path hash>`
- `app.kubernetes.io/part-of: llminferenceservice`

No new label keys, Pod labels, or Pod-template changes are introduced. The
NetworkPolicy metadata reuses the existing evictor marker and key labels.
The controller rejects missing or inconsistent identities, empty/invalid
selectors, selectors missing the evictor marker/key constraints, and Pod
templates that do not match their selector.

Both `Ingress` and `Egress` policy types are explicit, with no traffic rules.
An empty list denies traffic; a list containing an empty rule (`[{}]`) would
allow all traffic and is **not** used here.

This behavior is identical in default and strict ingress modes, independent
of tracing. Sidecar evictors (`pvc.spec`) share the vLLM Pod's network namespace
and policies; they do not receive separate policies.

## Reconciliation, ownership, and cleanup

Policy reconciliation runs after shared-evictor reconciliation, including
partial failures, and before routing and monitoring. It reads stored evictor
Deployments in the service's namespace, not just that service's desired targets.
Configuration, base-reference, or workload errors can prevent reaching this
stage. Failure for one evictor does not skip the other stored evictors.
A policy failure remains retryable even when the evictor stage also reports a
terminal configuration error; both error messages are retained, but only the
policy error is unwrapped. A terminal-only evictor failure keeps its original
terminal behavior.

Each policy is controller-owned by its **Deployment UID**, not by a consuming
LLMInferenceService or the shared ServiceAccount. Several services referencing
the same PVC/path share the Deployment and its policy. Removing or stopping
one consumer preserves coverage while another consumer still needs it.

Existing shared-evictor cleanup decides when the Deployment can be deleted.
Kubernetes garbage collection then removes the dependent policy. Service
NetworkPolicy finalization does not prune these policies. The same ownership
handles target removal, force-stop of the last active consumer, and Deployment
replacement/duplicate cleanup.

The controller watches evictor policy changes to repair deleted policies and
spec/managed-label drift. Foreign, ownerless, and old-UID policies at the
expected name are never adopted or removed: reconciliation reports an error
and retries. A policy belonging to a previous Deployment incarnation must be
removed by garbage collection before its replacement can be created.
Errors for a service's desired evictors mark its existing `PVCEvictorReady`
condition false; unrelated consumers do not gain a spurious readiness condition.

`LLMISVC_NETWORK_POLICY_DISABLED=true` suppresses creation and repair, just as
for the other managed policies. It does not purge policies already present.
Deployment garbage collection still applies. Monitoring disablement does not
disable these policies. Non-distro builds do not manage evictor policies.

## Optional/custom network traffic

NetworkPolicy permissions are additive. Operators supporting a custom evictor
or optional storage-event publishing must create separate, narrow allow
policies. Do not modify the generated policy: the controller repairs it.

For a TCP ZMQ publisher that **listens** in the evictor, subscribers initiate
connections to the evictor, so allow ingress from the intended subscriber Pods
on the configured TCP port. Return traffic on allowed connections is implicitly
permitted; this does not require an unrestricted egress grant.

The following is a **template**, not a ready-to-apply manifest. Substitute the
namespace, existing evictor key, actual subscriber application label value, and
actual configured numeric TCP port (for example using `envsubst`) before applying.
It permits only same-namespace subscribers. Adapt namespace selection explicitly
if your subscribers are elsewhere; do not broaden to every namespace.

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-evictor-storage-event-subscribers
  namespace: ${NAMESPACE}
spec:
  podSelector:
    matchLabels:
      serving.kserve.io/pvc-evictor: "true"
      serving.kserve.io/pvc-evictor-key: "${EVICTOR_KEY}"
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: "${SUBSCRIBER_APP}"
      ports:
        - protocol: TCP
          port: ${EVENT_PORT}
```

This is not an assumption that the shipped ODH image supports a particular
event endpoint or port. Verify the actual image and custom configuration.
Any other network dependency needs its own justified, narrowly scoped rule.

## Enforcement and validation limits

Policies are not firewall deny rules that override all other policies.
Another selecting allow policy can grant traffic, including broad grants
installed by an operator or user. Inspect the union of applicable policies.

Deployment creation, policy persistence, and CNI enforcement are not atomic.
Use namespace default-deny when protection is required before initial policy
convergence. Garbage collection also does not promise a particular ordering
between Pod and policy teardown.

Basic cleanup uses filesystem operations. Confirm the exact evictor image and
storage backend work with Pod networking denied; a mounted network filesystem
does not by itself prove either that Pod egress is needed or that it is not.

Unit tests and envtest verify selectors, ownership, lifecycle decisions, and
watch-driven repair. Envtest does not enforce packets, run Deployment controllers,
or perform garbage collection. Live CNI enforcement, actual image/storage
compatibility, and real garbage collection require separate cluster validation.
