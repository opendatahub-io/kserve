# Cluster Setup

Prepares a cluster for E2E testing by deploying the KServe manifests and the KServe Module Operator.

* Required cli:
```bash
./hack/setup/cli/install-helm.sh
```

* Create KIND Cluster:
```bash
./hack/setup/dev/manage.kind-with-registry.sh
```


## All-in-One

- install dependencies
- kserve module operator with E2E_IMG


```bash
KSERVE_NAMESPACE=kserve \
E2E_IMG=quay.io/jooholee/kserve-module-controller:20260806-1 \
make e2e-setup-kserve-module
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KSERVE_NAMESPACE` | `opendatahub` | Target namespace |
| `PLATFORM` | `xks` | Platform type - `xks` or `ocp` (automatically detect) |
| `E2E_IMG` | (unset) | Controller image to use (falls back to the kustomize default if omitted) |
| `SKIP_DEPS` | `false` | Skip dependency installation, deploy the operator only |
| `SKIP_KM_DEPLOY` | `false` | Install dependencies only, skip the kserve-module deploy |

## Dependencies only

Install the platform dependencies without deploying the kserve-module operator:

```bash
SKIP_KM_DEPLOY=true PLATFORM=ocp make e2e-setup-kserve-module
```

## Deploy the kserve module operator only

Deploy the kserve module operator when dependencies are already installed:

```bash
SKIP_DEPS=true PLATFORM=ocp make e2e-setup-kserve-module
```

## Cleanup

```bash
PLATFORM=ocp make e2e-cleanup-kserve-module
```

> See `kserve-module/tests/scripts/setup-cluster.sh` for the actual behavior.
