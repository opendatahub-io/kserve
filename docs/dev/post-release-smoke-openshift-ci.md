# Post-release kserve-module smoke (OpenShift CI)

RHOAIENG-85268: validate a **fresh OpenShift install** after an ODH release cut:

- `odh-model-controller` Running (no restarts)
- `KServeReady=True`
- one `LLMInferenceService` Ready

Tests and Make targets live in this repo (`post_release` marker, `make e2e-kserve-module-post-release`).
Orchestration is **OpenShift CI (Prow)** on Hypershift.

## What it does

1. You publish `quay.io/opendatahub/odh-kserve-module-operator:odh-vX.Y` and push tag `odh-vX.Y`.
2. On any open kserve PR, comment `/test e2e-kserve-module-post-release`.
3. Prow provisions an ephemeral Hypershift cluster (same profile as `e2e-kserve-module`).
4. `hack/ci/post-release-smoke.sh` picks the newest plain `odh-vX.Y` git tag (no `-ea`/`-rc`),
   checks out that tag, installs the **published** Quay operator image (never PR-built images),
   and runs `make e2e-kserve-module-post-release` (`post_release` pytest).

`/test` cannot take a tag argument. In CI the job always targets the **latest
plain** `odh-vX.Y` tag (no `-ea` / `-rc` / other suffix).

### Early-access / `-ea` / `-rc` images

If the release under test is tagged like `odh-v3.6-ea1` (Quay image ends with
`-ea1`, `-rc1`, etc.), **`/test` will not pick it**. Auto-resolve only matches
`^odh-v[0-9]+\.[0-9]+$`.

Run the **same** smoke script and Make targets with an explicit tag (local CRC /
any OpenShift kubeconfig — not via the GitHub comment):

```bash
export RELEASE_TAG=odh-v3.6-ea1
bash hack/ci/post-release-smoke.sh
```

That is the same flow as CI (checkout tag → published Quay image →
`make e2e-kserve-module-post-release`); only the trigger differs because Prow
`/test` cannot pass `RELEASE_TAG`.

## Trigger model

**Optional presubmit** on `opendatahub-io/kserve` (not a tag postsubmit):

```text
/test e2e-kserve-module-post-release
```

`always_run: false` / `optional: true` - never blocks merges.

| Event | Runs smoke? |
|-------|-------------|
| `/test e2e-kserve-module-post-release` on a PR | Yes (latest plain `odh-vX.Y`) |
| Push tag `odh-v3.6` alone | No |
| Merge PR to `master` | No |
| Re-run job in Prow UI | Yes |
| Validate `-ea`/`-rc` tag | Local: `RELEASE_TAG=odh-vX.Y-eaN bash hack/ci/post-release-smoke.sh` |

Prerequisites before `/test`:

- `quay.io/opendatahub/odh-kserve-module-operator:odh-vX.Y` exists for the newest tag
- That tag's commit includes `post_release` tests and `make e2e-kserve-module-post-release`

## Why not a tag postsubmit?

We wanted: push `odh-vX.Y` -> Prow runs automatically with that tag as `PULL_BASE_REF`.

That cannot merge in `openshift/release`:

1. **prowgen** always emits master postsubmits with `branches: ^master$`.
2. Hand-editing to `^odh-v\d+\.\d+$` fails **`generated-config`** (regen overwrites).
3. **`prow-config-semantics`** requires non-master branch jobs in a matching jobs filename;
   a tag regex cannot be generated from the master ci-operator config.

Optional `/test` reuses the existing Hypershift e2e stack and passes those checks.
Details: [ci-operator tag postsubmit investigation](./ci-operator-tag-postsubmit-branches.md).

## OpenShift CI vs Konflux

| | OpenShift CI (chosen) | Konflux |
|--|----------------------|---------|
| Trigger | `/test` on a kserve PR | Would need PAC comment / PipelineRun param |
| Cluster | Existing Hypershift workflow (`e2e-kserve-module`) | Would reimplement ephemeral OCP e2e |
| Published image only | Yes (script + Quay tag) | Possible, but new pipeline work |
| Tag on comment | No - always latest plain `odh-vX.Y` (`-ea` needs local `RELEASE_TAG`) | Easier to pass `release_tag` as a param |
| Maintenance | Same prow/ci-operator lane as other kserve e2e | Split across konflux-central + kserve + onboarder |
| openshift/release | One optional job (mergeable) | Avoids release PR, but duplicates e2e orchestration |

OpenShift CI wins for running the smoke; Konflux would win mainly for parameterized on-demand triggers.

## Local run

```bash
export RELEASE_TAG=odh-v3.6
bash hack/ci/post-release-smoke.sh
```

## OpenShift CI implementation

**PR:** [openshift/release#85320](https://github.com/openshift/release/pull/85320)

**File:** `ci-operator/config/opendatahub-io/kserve/opendatahub-io-kserve-master.yaml`

```yaml
- always_run: false
  as: e2e-kserve-module-post-release
  optional: true
  steps:
    # Hypershift workflow; test step runs:
    # bash hack/ci/post-release-smoke.sh
    workflow: hypershift-hostedcluster-workflow
```

```bash
cd openshift/release
make ci-operator-prowgen WHAT=opendatahub-io/kserve
make sanitize-prow-jobs WHAT=opendatahub-io/kserve
```

## Related repos

| Change | PR |
|--------|-----|
| `hack/ci/post-release-smoke.sh` + runbooks | [opendatahub-io/kserve#1982](https://github.com/opendatahub-io/kserve/pull/1982) |
| odh-model-controller runbook | [opendatahub-io/odh-model-controller#938](https://github.com/opendatahub-io/odh-model-controller/pull/938) |
| OpenShift CI job | [openshift/release#85320](https://github.com/openshift/release/pull/85320) |

## Release process checklist

1. Cut ODH release; publish `odh-kserve-module-operator:<tag>` on Quay
2. Tag kserve (`odh-vX.Y` or `odh-vX.Y-eaN`, etc.)
3. Trigger smoke:
   - Plain `odh-vX.Y`: `/test e2e-kserve-module-post-release` on any open kserve PR
   - `-ea`/`-rc`/suffixed tag: `export RELEASE_TAG=<tag> && bash hack/ci/post-release-smoke.sh` locally (same flow; `/test` ignores suffixes)
4. Watch Prow (plain tags) or local logs; green -> sign off
5. Re-run with `/test` / Prow UI, or re-run the local script with `RELEASE_TAG`

## Related

- Tests: `kserve-module/tests/e2e/test_release_validation.py`
- PR e2e (PR-built images): `/test e2e-kserve-module`
- JIRA: [RHOAIENG-85268](https://issues.redhat.com/browse/RHOAIENG-85268)
