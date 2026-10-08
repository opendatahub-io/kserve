# kserve-module upgrade e2e (OpenShift CI)

OpenShift CI entrypoint for rolling the **kserve-module controller image** from N
(base SHA) to N+1 (PR image) without disturbing operand workloads.

Planned as a separate Prow job (`e2e-kserve-module-upgrade`) alongside the
existing `e2e-kserve-module` sanity job. Requires a follow-up
[openshift/release](https://github.com/openshift/release) change to register the
job; this repository only provides the entrypoint script.

Depends on [#1966](https://github.com/opendatahub-io/kserve/pull/1966) for
`e2e-roll-kserve-module`, upgrade pytest markers, and `setup-cluster.sh`
`--skip-deps`.

## Flow

Controller-image-only upgrade: initial install uses **base (N) manifests** with the
**N image**; pytest and the roll step use the **PR tree**. Only the
module-controller image rolls from N to N+1.

```text
build N image from PULL_BASE_SHA in test pod
  -> publish N to cluster-pullable registry
  -> checkout base tree (PULL_BASE_SHA)
  -> e2e-setup with N image (base manifests)
  -> checkout PR tree (PULL_PULL_SHA)
  -> pre_upgrade
  -> e2e-roll to N+1 (KSERVE_MODULE_CONTROLLER_IMAGE)
  -> post_upgrade (module-controller roll check, ocp_only serving tests)
```

Unlike other kserve OCP CI jobs, this builds the N image in the test pod because
ci-operator only produces the PR (N+1) `KSERVE_MODULE_CONTROLLER_IMAGE`. The N
image must be pushed to the guest cluster registry (`oc registry login` +
`podman push`) so worker nodes can pull it for the initial Deployment.

The script sets `KSERVE_MODULE_UPGRADE_IMAGE` to `KSERVE_MODULE_CONTROLLER_IMAGE`
before the roll and post-upgrade phases so `test_module_controller_rolled` can
verify the Deployment image and pod UID changed.

## What this job enforces (vs xks GHA)

The required GitHub Actions xks job exercises operand identity and the
module-controller roll but skips `ocp_only` serving tests. This OCP job runs the
full upgrade suite, including:

- Real sklearn ISVC predict (pre/post)
- ISVC/LLMISVC workload pod UID survival
- Background ISVC health probe during roll
- LLMISVC WorkloadsReady continuity
- Part B: new ISVC/LLMISVC creation

See `kserve-module/docs/tests/test.km-e2e.md` for the full required-CI matrix.

## Usage

```bash
make e2e-kserve-module-upgrade-ocp
```

Required env: `PULL_BASE_SHA`, `PULL_PULL_SHA`, `KSERVE_MODULE_CONTROLLER_IMAGE`.

Optional: `KSERVE_MODULE_BASE_IMAGE` (pre-published pullable N image ref, skips
build/push), `REGISTRY_NAMESPACE` (default `opendatahub`).
