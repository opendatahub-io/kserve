# Test selector and Gatekeeper design

## Scope

The selector is a pure analysis boundary: changed paths plus trusted config and
mapping produce a TestSelection. It never clones repositories, posts comments,
dispatches Prow jobs, polls statuses, or receives credentials.

The ODH integration is an OpenShift CI/Prow Gatekeeper. GitHub Actions is not
the E2E dispatch or aggregation path.

```text
required Prow test-selector-gatekeeper
  pre: evaluator      protected image, no credential
  test: dispatcher    protected image, GitHub App secret at runtime
```

The stable image is promoted from protected odh/master; the PR cannot replace
the selector used to gate itself. Evaluator and dispatcher exchange only
selection.json.

## Selector model

learn builds a Mapping from the merged repository:

1. go list -json ./... discovers internal packages and dependency edges.
2. Go source patterns discover entrypoints, CRDs, controllers, watched CRDs,
   and framework implementations.
3. Python AST analysis discovers packages, E2E markers, CRD constructors,
   frameworks, and model formats.
4. Config/chart/hack discovery maps paths to CRD kinds.
5. The builder writes a complete mapping only after discovery succeeds.

Non-zero, timed-out, malformed, or empty Go discovery is an error. A mapping
loaded for query is validated for required fields, internal package mappings,
dependency consistency, and symlink/integrity hazards.

query processes each changed path, walks reverse Go dependencies, maps
entrypoints to CRDs, and maps CRDs/frameworks to E2E markers. Rules come from
the explicit --config file. No repository-local override is imported unless
the caller explicitly passes that file.

The selector has one E2E marker set. Prow routing is an adapter concern:

| Prow command | Context | Target | Expression |
| --- | --- | --- | --- |
| /test e2e-graph | ci/prow/e2e-graph | e2e-graph-ocp | graph |
| /test e2e-raw | ci/prow/e2e-raw | e2e-raw-ocp | raw or rawcipn |
| /test e2e-predictor | ci/prow/e2e-predictor | e2e-predictor-ocp | predictor or kserve_on_openshift |
| /test e2e-llm-inference-service | ci/prow/e2e-llm-inference-service | e2e-llmisvc-ocp | llmisvc_core and cluster_cpu and not pvc_storage |

The query job protocol emits every job boolean and one JSON object with bounded
selector reasons. The evaluator carries that record into artifact diagnostics.

Routing is intentionally an over-approximation. TestSelection contains a union
of markers from all affected tests, not a marker assignment for each test.
Accordingly, a job expression matches when any of its positive markers is in
that union; ``and`` requirements and ``not`` exclusions cannot safely suppress
a job. Full boolean evaluation over the union could create false negatives.

## Conservative rules

The invariant is no false negative: uncertainty widens selection.

- Unknown, deleted, or rename-old Go paths trigger all Go, Python, and E2E.
- An unknown python/<package>/... path triggers all coverage.
- Unknown source/configuration paths trigger all coverage unless explicitly
  ignorable by trusted config.
- Shared Go dependencies, SDK changes, webhook changes, and all-entrypoint
  reachability widen to all relevant E2Es.
- Known framework/server paths can select a narrower marker set.

TestSelection.reasons records the classification trace. Query output is
side-effect free; generated mappings and changed-file lists are caller-owned
paths, normally under evaluator-controlled /tmp.

## Gatekeeper trust boundaries

Trusted inputs are the promoted image, packaged config, packaged job map and
schema, evaluator-owned mapping, authenticated JOB_SPEC, and authenticated
GitHub/Prow responses. PR files and changed paths are data, not trusted config.

### Evaluator

The evaluator accepts only a Prow JOB_SPEC for opendatahub-io/kserve, base ref
master, one pull, and lowercase full SHAs. It clones publicly, fetches exact
base and PR refs, verifies the fetched head, computes name-status changes with
rename paths, checks out base, and merges the PR without committing. It runs
trusted learn and query, then atomically writes selection.json to shared and
artifact directories.

For a valid identity, clone/dependency/selector uncertainty produces
fallback-all. Merge conflict is a hard failure. Invalid identity produces no
dispatchable artifact.

### Dispatcher

The dispatcher validates schema, identity, mode, unique job names, fallback
completeness, and the live PR head. It accepts only the four allowlisted jobs
and takes command/context/target data from ocp_jobs.json, never selector text.
It reuses pending/success Prow contexts when safe, posts new allowlisted /test
commands only when needed, polls exact https://prow.ci.openshift.org/
contexts, and aggregates child results.

Only this step may receive the dedicated GitHub App credential through a
runtime secret mount. The credential is not installed by this repository,
baked into the image, or exposed to the evaluator. A missing credential,
GitHub API error, missing context, child failure, head change, or timeout fails
Gatekeeper. An empty selected set succeeds without posting a command.

## Image contract

gatekeeper/Dockerfile uses the public, digest-pinned golang:1.26.7-bookworm
image, matching Go 1.26.7 without requiring OpenShift registry credentials. It
installs Python 3.11, PyYAML, cryptography, pytest, and jsonschema from pinned
requirements, plus Git, jq, curl, and OpenSSL. The
trusted package is staged under /opt/test-selector and loaded with PYTHONPATH;
PYTHONSAFEPATH=1 prevents cwd shadowing. The image runs non-root and directs
Go, Python, and home/cache writes to /tmp.

No checkout tests or credentials are copied into the image. The CI unit step
can run PR checkout pytest files against the PR-built packaged runtime; the
production Gatekeeper steps use the promoted trusted image.

## Local validation

```bash
GOCACHE=/tmp/kserve-test-selector-gocache GOFLAGS=-buildvcs=false \
  PYTHONPATH=tools pytest -q tools/test_selector/tests \
  tools/test_selector/gatekeeper
ruff check tools/test_selector
git diff --check
```

The packaging smoke test stages the trusted package in a temporary import root,
runs both module CLIs from a hostile cwd, and verifies the dispatcher can load
cryptography. A local image build additionally requires Podman/Docker and
network access to the public Go base image; no GitHub credential is needed.
