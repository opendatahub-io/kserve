# E2E Testing

## Test responsibility boundary

This module E2E suite verifies the **orchestration contract**: enabling the module deploys the expected operands and wires them up correctly. 
It does not test what the operands themselves do once deployed.

What this suite owns:

- Operand deployment per platform (create/update/delete, GC, CRD preservation)
- Status/condition reporting and platform version transitions
- **Webhook registration**: each platform-expected Validating/Mutating webhook
  config exists, has a non-empty `caBundle`, and its `clientConfig.service` names
  the owning component's service (xks: llmisvc only; ocp: kserve, llmisvc,
  odh-model-controller).

Out of scope here (operand-level concerns, not the module's orchestration
contract):

- Model serving end to end and endpoint reachability (exception: the
  [module upgrade e2e](#module-upgrade-e2e-rhoaieng-82811) validates minimal
  workload health on OCP during a module image roll)
- **Webhook functional behavior**: whether a webhook actually rejects an invalid
  InferenceService or applies defaults. This suite checks only that the webhooks
  are registered and wired, not what they do.

## Prerequisites

- kind or minikube cluster running
- kubectl, kustomize, helm installed
- Python 3.9+ with `pytest` and `pyyaml`
- Controller image (build or use existing)

## Quick Start

```bash
export KO_DOCKER_REPO=quay.io/your-org
export TAG=latest
export PLATFORM=xks  # xks or ocp

# 1. Build and push controller image
make docker-build-kserve-module
make docker-push-kserve-module

# 2. Setup cluster and deploy controller with the built image
make e2e-setup-kserve-module E2E_IMG=${KO_DOCKER_REPO}/kserve-module-controller:${TAG}

# 3. Run tests
make e2e-kserve-module

# 4. Cleanup
make e2e-cleanup-kserve-module
```

## Platforms

| Platform | Flag | Dependencies installed via |
|----------|------|--------------------------|
| xks | `PLATFORM=xks` (default) | Helm scripts |
| ocp | `PLATFORM=ocp` | OLM subscriptions |

## Test Markers

- `sanity` - core lifecycle tests (create, update, delete, CEL validation)
- `pre_upgrade` / `post_upgrade` - module image upgrade tests (RHOAIENG-82811)
- `post_release` - post-ODH-release smoke (OMC Running, KServeReady, one LLMISVC Ready)

Run specific markers:

```bash
make e2e-kserve-module
make e2e-kserve-module PYTEST_ARGS='-m pre_upgrade --pre-upgrade'
make e2e-kserve-module PYTEST_ARGS='-m post_upgrade --post-upgrade'
PLATFORM=ocp make e2e-kserve-module-post-release
```

## Module upgrade e2e (RHOAIENG-82811)

Validates that rolling the **kserve-module controller image** (N → N+1 via
`deploy-kserve-module` / SSA manifest re-apply) does not disturb operand CRs or
running services.

| Ticket | Implementation |
| --- | --- |
| N = main, N+1 = PR | CI builds `e2e-base` from base SHA + `e2e` from PR HEAD |
| Part A: no disruption | Pre: deploy ISVC/LLMISVC, start background ISVC health probe, capture baseline. Post: probe clean, operand pods unchanged, controllers Available, Kserve Ready |
| Part B: new workloads | Post: create fresh ISVC + LLMISVC, verify Ready and serve |
| CI two images | `.github/workflows/e2e-test-kserve-module.yml` |

Tests live in `kserve-module/tests/e2e/upgrade/test_upgrade.py`.

### CI flow (xks)

```text
build N (main) + N+1 (PR) → install N → pre_upgrade → e2e-roll N+1 → post_upgrade → e2e-kserve-module
```

On xks, ISVC/LLMISVC serving tests are skipped (`ocp_only`); operand pod identity
and Kserve Ready are still checked.

### OpenShift dev cluster (platform-managed / DSC)

Do not run `e2e-setup-kserve-module` on DSC-owned clusters. Roll the module
controller image via the ODH subscription env override (DSC reconciles the
deployment; `oc set image` alone will be reverted):

```bash
export IMG=quay.io/<org>/kserve-module-controller:<tag>

oc patch subscription opendatahub-operator -n openshift-operators --type=merge -p "{
  \"spec\": {\"config\": {\"env\": [{
    \"name\": \"RELATED_IMAGE_ODH_KSERVE_MODULE_OPERATOR_IMAGE\",
    \"value\": \"${IMG}\"
  }]}}
}"

oc rollout status deployment/kserve-module-controller-manager -n opendatahub --timeout=300s
```

Baseline ConfigMap: `km-upgrade-baseline` in namespace `km-upgrade-e2e`.

## Post-Release Validation

After cutting an ODH release tag (e.g. `odh-v3.5`), validate a **fresh OpenShift
install**: odh-model-controller Running, KServeReady=True, then one
LLMInferenceService reaches Ready=True.

`odh-model-controller` and the LLMISVC smoke are OpenShift-only (`ocp_only`).
Minikube/`xks` skips them. Same cluster convention as Prow `e2e-kserve-module`
and Konflux group testing (`PLATFORM=ocp`):

```bash
export RELEASE_TAG=odh-v3.5
make e2e-setup-kserve-module \
  PLATFORM=ocp \
  E2E_IMG=quay.io/opendatahub/odh-kserve-module-operator:${RELEASE_TAG}
make e2e-kserve-module-post-release
```

CI: run from [odh-model-controller](https://github.com/opendatahub-io/odh-model-controller)
via `.github/workflows/post-release-smoke.yaml` (`workflow_dispatch`). It checks
out this repo at the release tag, uses `PLATFORM=ocp`, and runs the make targets
above. GitHub-hosted runners cannot provision OpenShift; the workflow expects an
existing OpenShift kubeconfig on the runner.

## Make Targets

| Target | Description |
|--------|-------------|
| `e2e-setup-kserve-module` | Install dependencies and deploy controller (image N) |
| `e2e-roll-kserve-module` | Re-deploy controller image only (upgrade to N+1) |
| `e2e-kserve-module` | Run E2E tests (`-m "not post_release"`; upgrade tests skipped unless `PYTEST_ARGS` sets `--pre-upgrade` / `--post-upgrade`) |
| `e2e-kserve-module-post-release` | Run post-release validation (`-m post_release`) |
| `e2e-cleanup-kserve-module` | Uninstall controller and dependencies |
