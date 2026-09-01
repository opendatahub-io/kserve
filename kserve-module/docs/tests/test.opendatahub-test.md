# Run opendatahub-tests (Model Serving)

Runs the KServe model-serving suite from the external
[opendatahub-tests](https://github.com/opendatahub-io/opendatahub-tests) repo
against a cluster where the KServe component is already installed.

> **Prerequisite:**
> - Cluster set up - see [../prerequisite/setup.cluster.md](../prerequisite/setup.cluster.md)
> - KServe installed via the kserve-module operator - see [../deploy/deploy.km.md](../deploy/deploy.km.md)
> - `uv` installed

## Setup

```bash
cd /tmp
git clone git@github.com:opendatahub-io/opendatahub-tests.git
cd opendatahub-tests
```

## Run kserve-specific tests

```bash
uv run pytest tests/model_serving/model_server/kserve/ \
    --cluster-sanity-skip-rhoai-check \
    --tc=applications_namespace:opendatahub \
    --tc=use_unprivileged_client:False
```

## Run all kserve tests

```bash
uv run pytest tests/model_serving/model_server/ \
    --cluster-sanity-skip-rhoai-check \
    --tc=applications_namespace:opendatahub \
    --tc=use_unprivileged_client:False
```
