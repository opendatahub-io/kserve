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

Validates that rolling the **kserve-module controller image** (base -> upgrade)
does not disturb operand CRs or running services. The roll is triggered by the
`e2e-roll-kserve-module` Make target (controller image update plus embedded
manifest re-apply via `setup-cluster.sh --skip-deps`).

| Ticket | Implementation |
| --- | --- |
| base = merge-base/main, upgrade = PR HEAD | CI builds `e2e-base` from base SHA + `e2e` from PR HEAD |
| Part A: no disruption | Pre: deploy ISVC/LLMISVC, start background ISVC health probe, capture baseline. Post: verify module-controller upgrade image + new pod UID, probe clean, operand pods unchanged, controllers Available, Kserve Ready |
| Part B: new workloads | Post: create fresh ISVC + LLMISVC, verify Ready and serve |
| CI two images | `.github/workflows/e2e-test-kserve-module.yml` |

Tests live in `kserve-module/tests/e2e/upgrade/test_upgrade.py`.

Set `KSERVE_MODULE_UPGRADE_IMAGE` to the same ref passed as `E2E_IMG` to
`e2e-roll-kserve-module` before running `post_upgrade` tests (GHA sets this
automatically).

### CI flow (xks)

```text
build base (main) + upgrade (PR) -> install base manifests -> pre_upgrade -> e2e-roll upgrade -> post_upgrade -> e2e-kserve-module
```

### Required CI guarantees (GitHub Actions xks)

| Guarantee | Enforced in required CI? |
| --- | --- |
| Base and upgrade controller images differ | Yes (`e2e-test-kserve-module.yml`) |
| Module-controller Deployment uses upgrade image after roll | Yes (`test_module_controller_rolled`, needs `KSERVE_MODULE_UPGRADE_IMAGE`) |
| Module-controller pod UID changes after roll | Yes (`test_module_controller_rolled`) |
| Kserve CR stays Ready with same UID | Yes |
| Operand controller pods (kserve/llmisvc) keep same UID | Yes |
| Operand controllers reach Available | Yes |
| ISVC sklearn predict request (pre/post) | **No** - `ocp_only`; skipped on xks |
| ISVC/LLMISVC workload pod UID survival | **No** - `ocp_only`; skipped on xks |
| ISVC background health probe during roll | **No** - `ocp_only`; skipped on xks |
| LLMISVC WorkloadsReady continuity | **No** - `ocp_only`; skipped on xks |
| Part B: new ISVC/LLMISVC creation | **No** - `ocp_only`; skipped on xks |

On xks (minikube CI), `ocp_only` tests are skipped because vanilla k8s lacks the
OpenShift routes and serving stack those workloads need. Operand identity, Kserve
Ready, and the module-controller image roll are still exercised.

### OpenShift serving continuity (not in required CI yet)

The `ocp_only` ISVC/LLMISVC upgrade tests (real sklearn inference, workload pod
UID survival, background probe, LLMISVC WorkloadsReady) are implemented in
`test_upgrade.py` but are **not** enforced by the required GitHub Actions job.
They must be run on OpenShift (`PLATFORM=ocp`). A dedicated Prow job and
`run-kserve-module-upgrade-e2e.sh` entrypoint are planned in a follow-up OCP CI
PR (openshift/release registration required). Until that job is registered,
serving continuity during module upgrade is validated manually on OpenShift dev
clusters.

### OpenShift dev cluster (platform-managed / DSC)

Do not run `e2e-setup-kserve-module` on DSC-owned clusters. Roll the module
controller image via the ODH subscription env override. The DataScienceCluster
operator owns the deployment, so a direct `oc set image` or manifest-only change
is rolled back on the next reconcile. The subscription `RELATED_IMAGE_*` env
persists the desired image across reconciles:

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

CI: OpenShift CI (Prow) optional `/test e2e-kserve-module-post-release` - after
publishing an `odh-vX.Y` or `odh-vX.Y-eaN`/`-rcN` tag and Quay image, comment that
on any open PR. Uses the **newest** matching tag (version sort: `-ea` of `X.Y`
ranks above plain `X.Y`; next minor ranks above prior `-ea`; never PR-built
images). Pin with `export RELEASE_TAG=<tag> && bash hack/ci/post-release-smoke.sh`.
See
[post-release smoke runbook](../../../docs/dev/post-release-smoke-openshift-ci.md).

Local or scripted (same commands as CI):

```bash
export RELEASE_TAG=odh-v3.5
bash hack/ci/post-release-smoke.sh
```

Release-process overview: [odh-model-controller post-release smoke doc](https://github.com/opendatahub-io/odh-model-controller/blob/incubating/docs/post-release-kserve-smoke.md).

## Make Targets

| Target | Description |
|--------|-------------|
| `e2e-setup-kserve-module` | Install dependencies and deploy controller (image N) |
| `e2e-roll-kserve-module` | Re-deploy controller image only (roll to upgrade image) |
| `e2e-kserve-module` | Run E2E tests (`-m "not post_release"`; upgrade tests skipped unless `PYTEST_ARGS` sets `--pre-upgrade` / `--post-upgrade`) |
| `e2e-kserve-module-post-release` | Run post-release validation (`-m post_release`) |
| `e2e-cleanup-kserve-module` | Uninstall controller and dependencies |
