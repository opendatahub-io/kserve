# Deploying the KServe Module Operator

Entry point for deploying the KServe Module Operator. Pick the method that fits your goal.

## Prerequisite

Set up the cluster (dependencies + namespace) first:

- [../prerequisite/setup.cluster.md](../prerequisite/setup.cluster.md) - install platform dependencies and prepare the cluster

## Deployment Methods

| Method | When to use | Guide |
|--------|-------------|-------|
| **Direct** | Local dev - build a custom controller image and deploy with kustomize | [deploy.km.directly.md](./deploy.km.directly.md) |
| **Official FBC (RHOAI)** | Test against a released RHOAI catalog on OpenShift | [deploy.km.rhoai-official-fbc.md](./deploy.km.rhoai-official-fbc.md) |
| **Official FBC (ODH)** | Test against a released OpenDataHub catalog on OpenShift | [deploy.km.odh-fbc.md](./deploy.km.odh-fbc.md) |
| **Custom FBC** | Build your own bundle + catalog (e.g. upgrade testing) | [deploy.km.custom-fbc.md](./deploy.km.custom-fbc.md) |

## Testing

After deploying, run the E2E suite:

```bash
PLATFORM=ocp make e2e-kserve-module
```
