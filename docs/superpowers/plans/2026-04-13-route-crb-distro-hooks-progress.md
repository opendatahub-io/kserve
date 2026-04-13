---

## jira: RHOAIENG-56270

# Route & CRB Distro Hooks — Implementation Progress

**Plan:** `docs/superpowers/plans/2026-04-13-route-crb-distro-hooks.md`
**Branch:** `rhoaieng-56729`
**Date:** 2026-04-13

## What and Why

OpenShift Route creation and auth-delegation ClusterRoleBinding management for raw InferenceServices currently live in `odh-model-controller`. This creates cross-controller race conditions and forces ODH-specific logic into shared files (`kube_ingress_reconciler.go`, `utils.go`), causing upstream rebase friction.

This work moves that logic into kserve behind `//go:build distro` tags using three hook pairs (`_default.go` / `_ocp.go`):

1. **Factory hook** — selects `RawOCPRouteReconciler` instead of `RawIngressReconciler` in distro builds (when Gateway API is disabled)
2. **Controller setup hook** — registers Route API scheme and `Owns(&routev1.Route{})` watch
3. **Platform permissions hook** — manages `system:auth-delegator` ClusterRoleBindings for auth-enabled ISVCs

Shared files are reverted to match upstream kserve/master.

## Status

Tasks 1–9 of the plan are complete with individual atomic commits. All new code compiles in both distro and non-distro modes. Unit tests (18 total: 10 for Route reconciler, 8 for CRB) pass.

**Blocked on:** Ginkgo integration test updates in `rawkube_controller_test.go` (Task 10).

## Test Fixes Already Applied

### Non-distro (`make test GOTAGS=""`) — passing (113/113)

Reverting `kube_ingress_reconciler.go` to upstream changed how `status.URL` is computed. The upstream `createRawURL` returns a domain-based URL (e.g. `raw-foo-default.example.com`) while the ODH version (`createRawURLODH`) returned the in-cluster service hostname (e.g. `raw-foo-predictor.default.svc.cluster.local`).

Three tests in `rawkube_controller_test.go` had expectations matching the old ODH URL format. Updated:


| Test                                                             | Old expected URL host                                             | New expected URL host                                            |
| ---------------------------------------------------------------- | ----------------------------------------------------------------- | ---------------------------------------------------------------- |
| "Should have ingress/service/deployment/hpa created" (line 8313) | `raw-foo-predictor.default.svc.cluster.local`                     | `raw-foo-default.example.com`                                    |
| "Should have ingress/.../configMap SAR created" (line 8861)      | `raw-auth-default.example.com` (scheme `https`, addr port `8443`) | `raw-auth-default.example.com` (scheme `http`, addr port `8080`) |
| "Should include port 8080 in status.address.url" (line 10223)    | `raw-headless-port-predictor.default.svc.cluster.local`           | `raw-headless-port-default.example.com`                          |


The address portion (service hostname, headless port) was already correct — only the top-level `status.URL` and auth scheme/port changed.

## Remaining: Distro Ginkgo Integration Tests

### Root cause

In distro mode, `factory_ocp.go` returns `RawOCPRouteReconciler` for Standard/LegacyRawDeployment ISVCs when Gateway API is disabled. The existing Ginkgo integration tests were written against the old ODH-modified `RawIngressReconciler` which:

- Created Kubernetes Ingress objects and set URL from `createRawURLODH`
- Handled auth URL/Address overrides inline
- Read Routes created externally by odh-model-controller

Now in distro mode, the OCP route reconciler creates Routes (not Ingress), sets URL from Route admission status, and manages the Route lifecycle directly.

### Failure categories (~25 tests)


| Category                                                             | Fix needed                                            |
| -------------------------------------------------------------------- | ----------------------------------------------------- |
| Tests asserting Kubernetes Ingress creation (gateway API disabled)   | Assert Route creation instead, or skip Ingress checks |
| Tests expecting domain-based URL without an admitted Route           | Simulate Route admission in test setup                |
| Tests that manually create Routes (old odh-model-controller pattern) | Remove manual Route creation; let reconciler own it   |
| Tests checking auth URL/Address format                               | Update expectations to match OCP reconciler output    |
| Cascade failures from shared test state                              | Will resolve once root causes are fixed               |


### Fix approach

Update failing integration tests to work with the OCP route reconciler: simulate OpenShift router by creating admitted Routes in test setup, assert Route objects instead of Ingress objects, and validate URL/Address from Route admission status.

## File Summary

All paths relative to `pkg/controller/v1beta1/inferenceservice/`.

### New files


| File                                                  | Build tag | Purpose                                                  |
| ----------------------------------------------------- | --------- | -------------------------------------------------------- |
| `reconcilers/factory_default.go`                      | `!distro` | No-op ingress hook                                       |
| `reconcilers/factory_ocp.go`                          | `distro`  | Returns OCP route reconciler (when gateway API disabled) |
| `reconcilers/ingress/ocp_route_reconciler.go`         | `distro`  | Route lifecycle reconciler                               |
| `reconcilers/ingress/ocp_route_reconciler_test.go`    | `distro`  | 10 unit tests                                            |
| `controller_setup_default.go`                         | `!distro` | No-op setup hook                                         |
| `controller_setup_ocp.go`                             | `distro`  | Route scheme + Owns watch                                |
| `platform_permissions_default.go`                     | `!distro` | No-op permissions hook                                   |
| `platform_permissions_ocp.go`                         | `distro`  | CRB reconciliation                                       |
| `platform_permissions_ocp_test.go`                    | `distro`  | 8 unit tests                                             |
| `distro/controller_rbac_ocp.go`                       | none      | RBAC markers for controller-gen                          |
| `config/overlays/odh/rbac/inferenceservice/role.yaml` | —         | Generated ClusterRole                                    |


### Modified files


| File                                             | Change                                                                 |
| ------------------------------------------------ | ---------------------------------------------------------------------- |
| `reconcilers/factory.go`                         | Added `resolveDistroIngressReconciler` call                            |
| `controller.go`                                  | Added `extendControllerSetup` and `reconcilePlatformPermissions` calls |
| `reconcilers/ingress/kube_ingress_reconciler.go` | Reverted to upstream                                                   |
| `utils/utils.go`                                 | Removed `GetRouteURLIfExists`                                          |
| `Makefile.overrides.mk`                          | Added ISVC distro RBAC target                                          |
| `rawkube_controller_test.go`                     | Updated 3 URL expectations for upstream behavior                       |


