# OpenShift CI Gatekeeper

This package is the trusted adapter around the CI-agnostic selector. It is
designed for the required OpenShift CI/Prow test-selector-gatekeeper job;
GitHub Actions does not dispatch or aggregate these E2Es.

## Runtime split

The promoted image contains the selector, packaged config.json, job map, and
selection schema.

- evaluate.py runs without a credential. It validates Prow JOB_SPEC, clones and
  merges exact base/PR refs, runs learn and query in evaluator-owned temporary
  paths, and writes selection.json.
- dispatch.py is the only credential-bearing step. It validates that artifact,
  uses only the four package-owned Prow jobs, posts missing /test commands,
  polls exact Prow contexts, and aggregates the result.

The dispatcher credential is a future/runtime OpenShift CI secret mount. It is
not included in this repository or baked into the image.

## Trusted job map

ocp_jobs.json is package-owned and requires each entry to contain:

```json
{
  "command": "/test e2e-graph",
  "context": "ci/prow/e2e-graph",
  "target": "e2e-graph-ocp",
  "expression": "graph"
}
```

The allowlist is exactly e2e-graph, e2e-raw, e2e-predictor, and
e2e-llm-inference-service. Selector output cannot add commands, contexts, or
targets. Selector uncertainty selects all four; malformed valid-identity
selection widens to all, while invalid identity or merge conflict fails before
dispatch.

## Image and local checks

The image uses the public, digest-pinned Go 1.26.7 Bookworm image, Python 3.11,
pinned PyYAML, cryptography, pytest, and jsonschema, plus Git, jq, curl, and
OpenSSL. It runs as a non-root UID, sets PYTHONSAFEPATH=1, keeps the official Go
toolchain on PATH, and writes caches only under /tmp.

From the repository root:

```bash
GOCACHE=/tmp/kserve-test-selector-gocache GOFLAGS=-buildvcs=false \
  PYTHONPATH=tools pytest -q tools/test_selector/tests \
  tools/test_selector/gatekeeper
ruff check tools/test_selector
git diff --check
podman build -f tools/test_selector/gatekeeper/Dockerfile .
```

The final image build needs a local container runtime and access to Docker Hub.
Unit tests and the packaging smoke test need no
GitHub credential.
