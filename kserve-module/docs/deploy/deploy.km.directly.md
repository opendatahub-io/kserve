# Deploy KServe Module Operator (Direct)

Builds a custom controller image and deploys it directly with kustomize. Fastest loop for local development.

> **Prerequisite:** the cluster must already be set up - see [../prerequisite/setup.cluster.md](../prerequisite/setup.cluster.md).

```bash
export KO_DOCKER_REPO=quay.io/jooholee
export KSERVE_MODULE_IMG=kserve-module-controller
export TAG=test

make docker-build-kserve-module docker-push-kserve-module
make deploy-kserve-module
```

The deployed image is `${KO_DOCKER_REPO}/${KSERVE_MODULE_IMG}:${TAG}`.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KO_DOCKER_REPO` | (required) | Target registry/org to push to, e.g. `quay.io/jooholee` |
| `KSERVE_MODULE_IMG` | `kserve-module-controller` | Image name (no tag) |
| `TAG` | (required) | Image tag |
| `ENGINE` | `docker` | Container engine - set to `podman` if preferred |

## Commands

| Target | Description |
|--------|-------------|
| `docker-build-kserve-module` | Build the controller image |
| `docker-push-kserve-module` | Push the image (builds first) |
| `deploy-kserve-module` | Set the image and apply the kustomize config |
