# kserve-module upgrade e2e (OpenShift CI)

OpenShift CI entrypoint for rolling the **kserve-module controller image** from N
(base SHA) to N+1 (PR image) without disturbing operand workloads.

This is wired as a separate Prow job (`e2e-kserve-module-upgrade`) alongside the
existing `e2e-kserve-module` sanity job. It does not replace that job.

## Flow

```text
build N in test pod (PULL_BASE_SHA) + N+1 from ci-operator
  → checkout N manifests → e2e-setup (N)
  → checkout PR manifests → pre_upgrade
  → e2e-roll (N+1) → post_upgrade
```

Unlike other kserve OCP CI jobs, this builds the N image in the test pod because
ci-operator only produces the PR (N+1) `KSERVE_MODULE_CONTROLLER_IMAGE`.

## Usage

```bash
make e2e-kserve-module-upgrade-ocp
```

Required env: `PULL_BASE_SHA`, `PULL_PULL_SHA`, `KSERVE_MODULE_CONTROLLER_IMAGE`.
