# Post-release kserve-module smoke (OpenShift CI)

RHOAIENG-85268: validate a **fresh OpenShift install** after an ODH release cut:

- `odh-model-controller` Running (no restarts)
- `KServeReady=True`
- one `LLMInferenceService` Ready

Tests and Make targets live in this repo (`post_release` marker, `make e2e-kserve-module-post-release`).
Orchestration is **OpenShift CI (Prow)** on Hypershift — not Konflux.

## Trigger model

**Tag postsubmit:** push git tag `odh-vX.Y` to `opendatahub-io/kserve` → Prow runs
`e2e-kserve-module-post-release` once. The tag name is the release parameter (no per-release
edit to `openshift/release`).

| Event | Runs smoke? |
|-------|-------------|
| Push tag `odh-v3.6` | Yes |
| Merge PR to `master` | No |
| Push branch | No |
| Re-run job in Prow/TestGrid | Yes (same tag, manual) |

Prerequisites when the tag is pushed:

- `quay.io/opendatahub/odh-kserve-module-operator:odh-vX.Y` exists
- Tag commit includes `post_release` tests and `make e2e-kserve-module-post-release`

## Local run (same as CI)

```bash
export RELEASE_TAG=odh-v3.6
bash hack/ci/post-release-smoke.sh
```

## OpenShift CI implementation

### 1. `openshift/release` PR (required)

**File:** `ci-operator/config/opendatahub-io/kserve/opendatahub-io-kserve-master.yaml`

Append test (after existing `e2e-kserve-module` block):

```yaml
- as: e2e-kserve-module-post-release
  postsubmit: true
  steps:
    allow_best_effort_post_steps: true
    cluster_profile: aws-opendatahub
    env:
      BASE_DOMAIN: openshift-ci-aws.rhaiseng.com
      COMPUTE_NODE_TYPE: m5.2xlarge
      HYPERSHIFT_NODE_COUNT: "3"
    post:
    - as: testlog-gather
      best_effort: true
      cli: latest
      commands: cp -v ${SHARED_DIR}/debuglog-*.log ${SHARED_DIR}/stdout-*.log ${SHARED_DIR}/stderr-*.log
        "${ARTIFACT_DIR}/" || true
      from: src
      resources:
        requests:
          cpu: 100m
      timeout: 1m0s
    - as: kserve-module-must-gather
      best_effort: true
      cli: latest
      commands: |-
        oc logs deployment/kserve-module-controller-manager -n opendatahub --tail=500 > "${ARTIFACT_DIR}/kserve-module-controller.log" 2>&1 || true
        oc logs deployment/odh-model-controller -n opendatahub --tail=200 > "${ARTIFACT_DIR}/odh-model-controller.log" 2>&1 || true
        oc get kserve -A -o yaml > "${ARTIFACT_DIR}/kserve-cr.yaml" 2>&1 || true
        oc get llminferenceservice -A -o yaml > "${ARTIFACT_DIR}/llminferenceservice.yaml" 2>&1 || true
        oc get pods -n opendatahub -o wide > "${ARTIFACT_DIR}/pods-opendatahub.txt" 2>&1 || true
        oc get events -n opendatahub --sort-by='.lastTimestamp' > "${ARTIFACT_DIR}/events-opendatahub.txt" 2>&1 || true
      from: src
      resources:
        requests:
          cpu: 100m
      timeout: 5m0s
    - as: openshift-must-gather
      best_effort: true
      cli: latest
      commands: oc adm must-gather --dest-dir "${ARTIFACT_DIR}/gather-openshift"
      from: src
      resources:
        requests:
          cpu: 100m
      timeout: 20m0s
    - chain: hypershift-hostedcluster-destroy
    test:
    - as: e2e-kserve-module-post-release
      cli: latest
      commands: |-
        pushd "$CLI_DIR"
        if [ ! -f kubectl ]; then ln -s oc kubectl; fi
        cat > kustomize <<'WRAP'
        #!/bin/sh
        exec oc kustomize "$@"
        WRAP
        chmod +x kustomize
        popd
        bash hack/ci/post-release-smoke.sh
      from: src
      resources:
        requests:
          cpu: 100m
    workflow: hypershift-hostedcluster-workflow
```

Then:

```bash
cd openshift/release
make update
```

**Tag branch filter (required follow-up):** `ci-operator-prowgen` emits postsubmit jobs with
`branches: ^master$`. Tag pushes use ref `odh-vX.Y`, not `master`. After `make update`, edit
the generated job in
`ci-operator/jobs/opendatahub-io/kserve/opendatahub-io-kserve-master-postsubmits.yaml`
for `branch-ci-opendatahub-io-kserve-master-e2e-kserve-module-post-release`:

```yaml
    branches:
    - ^odh-v\d+\.\d+$
```

Confirm with DPTP/RHOAI CI owners that tag pushes match this brancher regex on postsubmits.
Re-apply this edit if a future `make update` regen overwrites it (or ask DPTP for a supported
`branches` override on postsubmit tests).

**Note:** Postsubmit tests are not pj-rehearseable. Validate the job on the first real tag push
(or dry-run locally with `hack/ci/post-release-smoke.sh`).

### 2. kserve (this repo)

| Change | PR |
|--------|-----|
| `hack/ci/post-release-smoke.sh` | #1982 or follow-up |
| `kserve-module/docs/tests/test.km-e2e.md` | #1982 |
| This runbook | #1982 |

### 3. odh-model-controller

Update `docs/post-release-kserve-smoke.md` (#938): point to OpenShift CI tag postsubmit and
Prow re-run — remove Konflux `/post-release-smoke` and onboarder steps.

### 4. Close Konflux work

- Close **odh-konflux-central #655** (superseded by OpenShift CI)
- Do not run Konflux onboarder for post-release smoke

## Release process checklist

1. Cut ODH release; publish `odh-kserve-module-operator:odh-vX.Y` on Quay
2. Tag kserve: `git tag odh-vX.Y && git push origin odh-vX.Y`
3. Watch Prow: `branch-ci-opendatahub-io-kserve-master-e2e-kserve-module-post-release`
4. Green → sign off; red → inspect `${ARTIFACT_DIR}` (OMC logs, KServe CR, LLMISVC)
5. Re-run from Prow UI if needed (no new tag required)

## Related

- Tests: `kserve-module/tests/e2e/test_release_validation.py`
- PR e2e (not post-release): `/test e2e-kserve-module` on kserve PRs
- JIRA: [RHOAIENG-85268](https://issues.redhat.com/browse/RHOAIENG-85268)
