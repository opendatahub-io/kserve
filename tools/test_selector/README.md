# KServe test selector

The selector maps changed paths to Go, Python, and OpenShift E2E coverage. It is
a CI-agnostic analysis library and CLI: it does not dispatch jobs, use GitHub
credentials, or trust configuration from a pull request.

The ODH integration is OpenShift CI/Prow, not GitHub Actions E2E dispatch:

```text
Prow test-selector-gatekeeper
  ├─ evaluator (trusted image, no credential)
  │    clone base + PR, learn/query merged worktree
  │    -> SHARED_DIR/selection.json
  └─ dispatcher (trusted image, runtime GitHub App secret only)
       validate selection, post allowlisted /test commands, poll Prow
       -> aggregate Gatekeeper result
```

## Requirements

- Python 3.11 for the packaged Gatekeeper image.
- Go 1.26.7 for go list during learn.
- PyYAML for CRD/config discovery. The image also pins cryptography for
  GitHub App signing, plus pytest and jsonschema for the CI self-test.
- The image installs Git, jq, curl, and OpenSSL, runs non-root, and keeps
  Go/Python caches under /tmp. No credential is baked into it.

## Selector CLI

Run from the repository root:

```bash
export PYTHONPATH=tools

python -m test_selector --config tools/test_selector/config.json \
  learn --repo "$PWD" --output /tmp/kserve-mapping.json

git diff --name-only origin/master...HEAD > /tmp/changed-files.txt
python -m test_selector --config tools/test_selector/config.json \
  query --repo "$PWD" --mapping /tmp/kserve-mapping.json \
  --changed-files-file /tmp/changed-files.txt --format json
```

The global --config must be explicit for local alternatives; a
config.override.json is never imported implicitly. learn --output,
query --mapping, and query --changed-files-file keep generated and changed
file inputs outside the PR checkout when used by Gatekeeper.

For Prow matching, use the packaged gatekeeper/ocp_jobs.json:

```bash
python -m test_selector --config tools/test_selector/config.json \
  query --repo /tmp/kserve-merged --mapping /tmp/mapping.json \
  --changed-files-file /tmp/changed-files.txt \
  --match-jobs-file tools/test_selector/gatekeeper/ocp_jobs.json
```

This emits one job=true|false line for every map entry, followed by a bounded
JSON reasons record. The four trusted Prow names are e2e-graph, e2e-raw,
e2e-predictor, and e2e-llm-inference-service. Their command, context, target,
and expression are package-owned data, not selector output.

## Selection and conservative behavior

learn discovers Go entrypoints and dependency ownership, CRD/controller
relationships, E2E markers and constructors, Python packages, and config/chart
CRD relationships. query walks those relationships for each changed path.

The normal output is one selection object:

```json
{
  "go_tests": {"run": true, "packages": ["./cmd/llmisvc..."]},
  "python_tests": {"run": false},
  "e2e_tests": {"run": true, "markers": ["llminferenceservice", "cluster_cpu"]},
  "reasons": ["pkg/... -> entrypoint:./cmd/llmisvc -> e2e:..."]
}
```

The selector favors false positives over false negatives:

- unknown, deleted, or rename-old Go paths trigger all Go, Python, and E2E;
- an unmapped python/<package>/... path triggers all coverage;
- unknown source, config, chart, and infrastructure paths trigger all coverage;
- shared dependencies and SDK changes widen to all relevant E2Es;
- known framework/server paths may remain narrow.

Job expressions are routing metadata, not boolean filters over a single test.
The selected marker set is a union across affected tests, so a job is selected
when any positive marker named by its expression is affected. Missing
conjunctive markers and negative markers never exclude the job; interpreting
them against the union could omit an affected test. Shadow-mode reduction data
will show whether a future per-test marker model is worth the added complexity.

If a valid PR identity encounters a clone, dependency, or selector error, the
evaluator writes fallback-all. Invalid identity and merge conflicts fail
without a dispatchable artifact.

## Trust and maintenance

config.json, the job map, and the selection schema are packaged trusted inputs.
mapping.json is generated in evaluator-owned temporary storage and integrity
checked before use. The dispatcher accepts only the four allowlisted jobs and
constructs Prow commands from its own job map.

Local experimentation is opt-in: pass an alternative config explicitly with
--config. Do not edit or trust generated mapping.json from a PR checkout.
When controllers, CRDs, E2E suites, or Python servers change, rerun learn and
inspect reasons for representative paths.

## Tests and local image checks

```bash
GOCACHE=/tmp/kserve-test-selector-gocache GOFLAGS=-buildvcs=false \
  PYTHONPATH=tools pytest -q tools/test_selector/tests \
  tools/test_selector/gatekeeper
ruff check tools/test_selector
git diff --check
```

When a local container runtime and registry access are available:

```bash
podman build -f tools/test_selector/gatekeeper/Dockerfile .
```

The local suite needs no GitHub App or external credential.
