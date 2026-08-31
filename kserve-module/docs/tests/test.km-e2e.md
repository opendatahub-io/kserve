# E2E Testing

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
- `post_release` - post-ODH-release smoke (OMC Running, KServeReady, one LLMISVC Ready)

Run specific markers:

```bash
make e2e-kserve-module
PLATFORM=ocp make e2e-kserve-module-post-release
```

## Post-Release Validation

After cutting an ODH release tag (e.g. `odh-v3.5`), validate a **fresh OpenShift
install**: odh-model-controller Running, KServeReady=True, then one
LLMInferenceService reaches Ready=True.

`odh-model-controller` and the LLMISVC smoke are OpenShift-only (`ocp_only`).
Minikube/`xks` skips them. Same cluster convention as Prow `e2e-kserve-module`
and Konflux group testing (`PLATFORM=ocp`):

```bash
export KSERVE_RELEASE_TAG=odh-v3.5
make e2e-setup-kserve-module \
  PLATFORM=ocp \
  E2E_IMG=quay.io/opendatahub/odh-kserve-module-operator:${KSERVE_RELEASE_TAG}
make e2e-kserve-module-post-release
```

CI: `.github/workflows/post-release-e2e.yml` is `workflow_dispatch` and always
uses `PLATFORM=ocp`. GitHub-hosted runners cannot provision OpenShift, so the
job fails unless the runner already has an OpenShift kubeconfig.

## Make Targets

| Target | Description |
|--------|-------------|
| `e2e-setup-kserve-module` | Install dependencies and deploy controller |
| `e2e-kserve-module` | Run E2E tests (`-m "not post_release"`) |
| `e2e-kserve-module-post-release` | Run post-release validation (`-m post_release`) |
| `e2e-cleanup-kserve-module` | Uninstall controller and dependencies |
