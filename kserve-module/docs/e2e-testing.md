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

- `sanity` — core lifecycle tests (create, update, delete, CEL validation)

Run specific markers:

```bash
make e2e-kserve-module
```

## Make Targets

| Target | Description |
|--------|-------------|
| `e2e-setup-kserve-module` | Install dependencies and deploy controller |
| `e2e-kserve-module` | Run E2E tests |
| `e2e-cleanup-kserve-module` | Uninstall controller and dependencies |
