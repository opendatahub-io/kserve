# LLM Inference Service E2E Tests

llm-d provisioning is retired on this branch. These tests replace the former
managed scheduler, router, and workload provisioning scenarios. New services
are rejected even when no managed scheduler is requested.

The tests exercise the installed API server and webhooks and verify that:

- Creating either an `LLMInferenceService` or an `LLMInferenceServiceConfig`
  returns HTTP 403 with the retirement message.
- Admission applies in two independent namespaces and to both real and dry-run
  requests.
- Rejected requests leave no objects behind.

No model downloads, GPUs, router presets, or inference endpoints are needed.
The namespace fixtures clean up even if creation is unexpectedly admitted.

Run against a cluster with the PR controller image, CRDs, and webhooks installed:

```sh
pytest -m "llminferenceservice and cluster_cpu" test/e2e/llmisvc/
```

This is fresh-install admission coverage, not an upgrade test. Existing-object
spec/metadata/status/deletion behavior and unmanaged reconciliation are covered
by `cmd/manager/retirement_test.go` and
`pkg/controller/llmisvc/unmanaged_test.go`. An upgrade E2E test must seed workloads
before installing the retired controller; do not disable admission on a running
cluster to manufacture legacy objects.
