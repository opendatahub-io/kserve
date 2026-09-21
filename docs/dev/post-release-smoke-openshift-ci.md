# Post-release kserve-module smoke (OpenShift CI)

RHOAIENG-85268: validate a **fresh OpenShift install** after an ODH release cut:

- `odh-model-controller` Running (no restarts)
- `KServeReady=True`
- one `LLMInferenceService` Ready

Tests and Make targets live in this repo (`post_release` marker, `make e2e-kserve-module-post-release`).
Orchestration is **OpenShift CI (Prow)** on Hypershift.

## Trigger model

**Optional presubmit** on `opendatahub-io/kserve` (not a tag postsubmit):

```text
/test e2e-kserve-module-post-release
```

Comment that on any open kserve PR after the release tag exists and the operator
image is on Quay. The job is `always_run: false` / `optional: true` so it never
blocks merges.

`hack/ci/post-release-smoke.sh` resolves the tag as:

1. `RELEASE_TAG` env if set
2. else the newest `odh-vX.Y` tag (no `-ea` / `-rc` suffix)

Then it checks out that tag and installs
`quay.io/opendatahub/odh-kserve-module-operator:${RELEASE_TAG}` (never PR-built
images).

| Event | Runs smoke? |
|-------|-------------|
| `/test e2e-kserve-module-post-release` on a PR | Yes |
| Push tag `odh-v3.6` alone | No (OpenShift CI cannot host tag-regex postsubmits under master prowgen files) |
| Merge PR to `master` | No |
| Re-run job in Prow UI | Yes |

### Why not tag postsubmit?

`ci-operator-prowgen` always emits postsubmit `branches: ^master$` for the master
config. Hand-editing to `^odh-v\d+\.\d+$` fails `generated-config` and
`prow-config-semantics` (job would have to live in a non-master jobs file that
prowgen cannot produce for a tag regex). See
[ci-operator tag postsubmit investigation](./ci-operator-tag-postsubmit-branches.md).

Optional `/test` reuses the same Hypershift workflow as `e2e-kserve-module` and
passes openshift/release validation.

Prerequisites before `/test`:

- `quay.io/opendatahub/odh-kserve-module-operator:odh-vX.Y` exists
- Tag commit includes `post_release` tests and `make e2e-kserve-module-post-release`

## Local run (same as CI)

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
    allow_best_effort_post_steps: true
    cluster_profile: aws-opendatahub
    env:
      BASE_DOMAIN: openshift-ci-aws.rhaiseng.com
      COMPUTE_NODE_TYPE: m5.2xlarge
      HYPERSHIFT_NODE_COUNT: "3"
    # ... same Hypershift post steps as e2e-kserve-module ...
    test:
    - as: e2e-kserve-module-post-release
      cli: latest
      commands: |-
        # kubectl/kustomize shim ...
        bash hack/ci/post-release-smoke.sh
      from: src
    workflow: hypershift-hostedcluster-workflow
```

After editing config:

```bash
cd openshift/release
make ci-operator-prowgen WHAT=opendatahub-io/kserve
# or: make jobs
```

## Related repos

| Change | PR |
|--------|-----|
| `hack/ci/post-release-smoke.sh` + runbooks | [opendatahub-io/kserve#1982](https://github.com/opendatahub-io/kserve/pull/1982) |
| odh-model-controller runbook | [opendatahub-io/odh-model-controller#938](https://github.com/opendatahub-io/odh-model-controller/pull/938) |
| OpenShift CI job | [openshift/release#85320](https://github.com/openshift/release/pull/85320) |

## Release process checklist

1. Cut ODH release; publish `odh-kserve-module-operator:odh-vX.Y` on Quay
2. Tag kserve: `git tag odh-vX.Y && git push origin odh-vX.Y`
3. On any open kserve PR (or a small docs PR), comment `/test e2e-kserve-module-post-release`
4. Watch Prow: `pull-ci-opendatahub-io-kserve-master-e2e-kserve-module-post-release`
5. Green -> sign off; red -> inspect `${ARTIFACT_DIR}` (OMC logs, KServe CR, LLMISVC)
6. Re-run with `/test e2e-kserve-module-post-release` or Prow UI if needed

To force a non-latest tag locally: `export RELEASE_TAG=odh-v3.5`.

## Related

- Tests: `kserve-module/tests/e2e/test_release_validation.py`
- PR e2e (PR-built images): `/test e2e-kserve-module`
- JIRA: [RHOAIENG-85268](https://issues.redhat.com/browse/RHOAIENG-85268)
