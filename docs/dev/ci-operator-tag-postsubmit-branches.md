# ci-operator tag postsubmit: `branches` gap

We originally wanted a **tag postsubmit**: push `odh-vX.Y` on `opendatahub-io/kserve`
-> Prow runs `e2e-kserve-module-post-release` automatically.

That approach **cannot merge** in `openshift/release` today. Post-release smoke
instead uses an **optional master presubmit** (`/test e2e-kserve-module-post-release`).
See [post-release-smoke-openshift-ci.md](./post-release-smoke-openshift-ci.md).

## Why tag postsubmit fails CI

### 1. prowgen hardcodes master branches

`ci-operator-prowgen` generates master postsubmits with:

```yaml
branches:
- ^master$
```

from `zz_generated_metadata.branch: master` via `ExactlyBranch(info.Branch)`.

Hand-editing to `^odh-v\d+\.\d+$` fails `ci/prow/generated-config` (regen overwrites).

### 2. prow-config-semantics sharding

Jobs whose branch regex is not `master` must live in a matching jobs filename.
`^odh-v\d+\.\d+$` becomes label `odh-vd.d`, so the validator wants
`opendatahub-io-kserve-odh-vd.d-postsubmits.yaml` - which prowgen will not emit
from the master ci-operator config.

### 3. No `branches` field on ci-operator `Test`

Presubmits have `SkipBranches`; postsubmits have no `Branches` override on the
`Test` struct. A proper fix belongs in `openshift/ci-tools` (add `branches` and
wire `generatePostsubmitForTest`).

## Chosen workaround

Optional presubmit on master:

```yaml
- always_run: false
  as: e2e-kserve-module-post-release
  optional: true
```

Trigger with `/test e2e-kserve-module-post-release` after the tag and Quay image
exist. Script resolves newest `odh-vX.Y` tag (or `RELEASE_TAG`).

## Alternatives considered

| Approach | Verdict |
|----------|---------|
| Manual `branches` patch after prowgen | Fails generated-config / semantics (openshift/release#85320) |
| Hardcode `RELEASE_TAG` per release in config | Rejected - poor UX |
| New ci-operator config per `odh-vX.Y` | Only works if release creates a **branch**, not just a tag |
| Periodic + auto-discover latest tag | Possible later; laggy vs on-demand `/test` |
| Optional presubmit `/test` | **Chosen** - passes prowgen, same Hypershift e2e stack; CI always uses newest `odh-vX.Y` |
| Konflux PAC with `release_tag` param | Better tag UX, but duplicates Hypershift e2e orchestration |

## References

- [Prow postsubmit brancher](https://docs.prow.k8s.io/docs/jobs/)
- [ci-operator postsubmit tests](https://github.com/openshift/ci-docs/blob/main/content/en/architecture/ci-operator.md#post-submit-tests)
- kserve runbook: [post-release-smoke-openshift-ci.md](./post-release-smoke-openshift-ci.md)
