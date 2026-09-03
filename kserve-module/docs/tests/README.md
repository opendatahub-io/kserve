# KServe Module - Testing

Entry point for testing the KServe Module operator. Deploy first (see
[../deploy/deploy.km.md](../deploy/deploy.km.md)), then pick a suite.

## Test Suites

| Suite | What it covers | Guide |
|-------|----------------|-------|
| **KServe Module E2E** | Basic kserve-module E2E on XKS or OCP (`make e2e-kserve-module`) | [test.km-e2e.md](./test.km-e2e.md) |
| **ODH Operator E2E** | ODH operator E2E suite (kserve component) on XKS or OCP | [test.odh-operator-e2e.md](./test.odh-operator-e2e.md) |
| **opendatahub-tests** | Model-serving suite from the external opendatahub-tests repo | [test.opendatahub-test.md](./test.opendatahub-test.md) |
| **Upgrade** | Service impact during in-tree -> module migration + HTML report | [upgrade/README.md](./upgrade/README.md) |
