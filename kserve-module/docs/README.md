# KServe Module - Dev Guide

Guides for building, deploying, and testing the KServe Module operator.
Follow the flow: **prerequisite -> deploy -> test**.

## 1. Prerequisite

| Guide | Description |
|-------|-------------|
| [prerequisite/setup.cluster.md](./prerequisite/setup.cluster.md) | Set up the cluster (dependencies + namespace + deploy kserve module operator) |

## 2. Deploy

Start at [deploy/deploy.km.md](./deploy/deploy.km.md) - it indexes all deployment methods:

| Method | When to use |
|--------|-------------|
| [Direct](./deploy/deploy.km.directly.md) | Local dev - build a custom controller image and deploy with kustomize |
| [Official FBC (RHOAI)](./deploy/deploy.km.rhoai-official-fbc.md) | Test against a released RHOAI catalog on OpenShift |
| [Official FBC (ODH)](./deploy/deploy.km.odh-fbc.md) | Test against a released OpenDataHub catalog on OpenShift |
| [Custom FBC](./deploy/deploy.km.custom-fbc.md) | Build your own bundle + catalog, then deploy |

## 3. Test

Start at [tests/README.md](./tests/README.md) - it indexes all test suites:

| Suite | Description |
|-------|-------------|
| [KServe Module E2E](./tests/test.km-e2e.md) | Basic kserve-module E2E on XKS or OCP |
| [ODH Operator E2E](./tests/test.odh-operator-e2e.md) | ODH operator E2E suite (kserve component) |
| [opendatahub-tests](./tests/test.opendatahub-test.md) | Model-serving suite from the external opendatahub-tests repo |
| [Upgrade](./tests/upgrade/README.md) | Service impact during in-tree -> module migration |

## Reference

- [example-kserve-cr.yaml](./example-kserve-cr.yaml) - sample KServe CR
