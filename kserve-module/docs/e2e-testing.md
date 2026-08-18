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
- `post_release` - post-ODH-release validation (image tags, operand health, serving smoke)

Run specific markers:

```bash
make e2e-kserve-module
KSERVE_RELEASE_TAG=odh-v3.5 make e2e-kserve-module-post-release
```

## Post-Release Validation

After cutting an ODH release tag (e.g. `odh-v3.5`), validate the published
kserve-module operator image and gathered operands:

```bash
export KSERVE_RELEASE_TAG=odh-v3.5
make e2e-setup-kserve-module \
  PLATFORM=ocp \
  E2E_IMG=quay.io/opendatahub/odh-kserve-module-operator:${KSERVE_RELEASE_TAG}
make e2e-kserve-module-post-release
```

CI: `.github/workflows/post-release-e2e.yml` runs on `odh-v*` tag push or
`workflow_dispatch` (pulls the Quay image for the release tag).

## Make Targets

| Target | Description |
|--------|-------------|
| `e2e-setup-kserve-module` | Install dependencies and deploy controller |
| `e2e-kserve-module` | Run E2E tests |
| `e2e-kserve-module-post-release` | Run post-release validation (`-m post_release`) |
| `e2e-cleanup-kserve-module` | Uninstall controller and dependencies |
