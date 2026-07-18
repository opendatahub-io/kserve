# E2E Test OpenShift Compatibility Rules

These rules apply to **Python e2e test files** under `test/e2e/` - skip them for non-test diffs.

## Context - why these rules exist

The e2e tests run on both vanilla Kubernetes (upstream CI) and OpenShift (midstream CI). OpenShift
enforces SecurityContextConstraints (SCC) and may enable TLS for workload pods. Tests that work
upstream can silently fail on OpenShift with opaque symptoms (pods running but never ready, 600s
timeouts with no diagnostics).

## Rules

### 1. Never hardcode `runAsUser` / `runAsGroup` in test workload specs

OpenShift's `restricted-v2` SCC rejects explicit UIDs outside the namespace's assigned range.
The `fixtures.py` module already provides `LLMD_SIMULATOR_SECURITY_CONTEXT` gated behind
`RUN_AS_NON_ROOT` (set `true` in OpenShift CI by `run-e2e-tests.sh`). Import it instead of
defining a local copy.

**Bad** - breaks on OpenShift:
```python
SECURITY_CONTEXT = {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532}
```

**Good** - works everywhere:
```python
from .fixtures import LLMD_SIMULATOR_SECURITY_CONTEXT
```

### 2. Workloads with `command` override must handle TLS

When `enableLLMInferenceServiceTLS` is `true`, the controller configures HTTPS readiness probes
on port 8000. The default entrypoint wrapper passes `--ssl-certfile` / `--ssl-keyfile` to vLLM
automatically, but a `command` override bypasses it. The workload must serve TLS itself or the
probe fails indefinitely (`MinimumReplicasUnavailable`).

Use Go template conditionals in the `LLMInferenceServiceConfig` args - the controller renders
them via `ReplaceVariables`:

```python
args=[
    "--port", "8000",
    "{{ if .GlobalConfig.EnableTLS }}--ssl-certfile{{- end }}",
    "{{ if .GlobalConfig.EnableTLS }}/var/run/kserve/tls/tls.crt{{- end }}",
    "{{ if .GlobalConfig.EnableTLS }}--ssl-keyfile{{- end }}",
    "{{ if .GlobalConfig.EnableTLS }}/var/run/kserve/tls/tls.key{{- end }}",
]
```

The TLS volume mount and secret are provided by the well-known config template that the
controller auto-injects - no need to add them in the test config.

### 3. Auth test RBAC must target the LLMISVC namespace

The `test_case` fixture creates LLMISVCs in per-test namespaces (e.g.
`e2e-test-llm-auth-enabled-*`). SA/Role/RoleBinding for auth tests must be created in the same
namespace. The gateway AuthPolicy's SAR check extracts the namespace from the request URL path -
RBAC in a different namespace will never match.

Always pass `namespace=` explicitly to `create_service_account_with_inference_access` and
`cleanup_service_account` rather than relying on the `KSERVE_TEST_NAMESPACE` default.
