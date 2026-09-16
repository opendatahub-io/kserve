# ci-operator tag postsubmit: `branches` gap

Post-release smoke uses a **tag postsubmit**: push `odh-vX.Y` on `opendatahub-io/kserve` → Prow
runs `e2e-kserve-module-post-release`.

## The problem

`ci-operator-prowgen` generates postsubmit jobs with:

```yaml
branches:
- ^master$
```

(from `zz_generated_metadata.branch: master` via `jc.ExactlyBranch(info.Branch)` in
[`openshift/ci-tools/pkg/prowgen/prowgen.go`](https://github.com/openshift/ci-tools/blob/master/pkg/prowgen/prowgen.go))

ODH release cuts use **git tags** (`odh-v3.6`), not pushes to `master`. Prow matches postsubmit
`branches` regexes against the **ref name** on the push event. For a tag push, that ref is
`odh-v3.6`, which does **not** match `^master$` — so the job **never runs** unless we patch
the generated prow job.

## What we verified

| Layer | Behavior |
|-------|----------|
| **Prow postsubmit** | `branches` is a list of regexes matched against the push ref (tag names work) |
| **ci-operator `Test` struct** | Has `SkipBranches` for **presubmits only**; no `Branches` for postsubmits |
| **prowgen `generatePostsubmitForTest`** | Always sets `Brancher.Branches: []string{ExactlyBranch(info.Branch)}` |
| **Existing ODH pattern** | Release **branches** (e.g. `v1.23.0`) get a separate config file + `branches: ^v1\.23\.0$` — not suitable for many `odh-v*` **tags** on `master` |

## Workaround (current PR)

After `make ci-operator-prowgen WHAT=opendatahub-io/kserve`, manually edit:

`ci-operator/jobs/opendatahub-io/kserve/opendatahub-io-kserve-master-postsubmits.yaml`

```yaml
# job: branch-ci-opendatahub-io-kserve-master-e2e-kserve-module-post-release
branches:
- ^odh-v\d+\.\d+$
```

**Downside:** `make update` / prowgen regen **overwrites** this back to `^master$`. Re-apply
after every prowgen touching kserve, or add a CI check that fails if branches drift.

## Proper fix (proposed upstream)

Add to `openshift/ci-tools` `pkg/api/types.go` on `Test`:

```go
// Branches overrides postsubmit branch/tag regexes. When empty, prowgen uses
// ExactlyBranch(info.Branch) from zz_generated_metadata.
Branches []string `json:"branches,omitempty"`
```

Wire in `generatePostsubmitForTest`:

```go
branches := []string{jc.ExactlyBranch(info.Branch)}
if len(element.Branches) > 0 {
    branches = element.Branches
}
pj := &prowconfig.Postsubmit{
    ...
    Brancher: prowconfig.Brancher{Branches: branches},
}
```

Then in `opendatahub-io-kserve-master.yaml`:

```yaml
- as: e2e-kserve-module-post-release
  postsubmit: true
  branches:
  - ^odh-v\d+\.\d+$
  steps:
    ...
```

Prowgen would emit the correct `branches` and regen would be safe.

## Alternatives considered

| Approach | Verdict |
|----------|---------|
| Hardcode `RELEASE_TAG` per release in config | Rejected — poor UX |
| Optional presubmit `/test` | Uses PR code or needs tag pointer file — not tag postsubmit |
| New ci-operator config per `odh-vX.Y` release branch | Only works if release creates a **branch**, not just a tag |
| Periodic + `extra_refs` | Static tag in yaml — same hardcoding problem |

## Validation before first tag push

1. Merge `openshift/release` PR with config + patched `branches`.
2. Push a test tag (or re-run postsubmit from Prow after a tag push) and confirm the job triggers.
3. Confirm `PULL_BASE_REF=odh-vX.Y` in job logs and `hack/ci/post-release-smoke.sh` resolves the tag.

## References

- [Prow postsubmit brancher](https://docs.prow.k8s.io/docs/jobs/)
- [ci-operator postsubmit tests](https://github.com/openshift/ci-docs/blob/main/content/en/architecture/ci-operator.md#post-submit-tests)
- kserve runbook: [post-release-smoke-openshift-ci.md](./post-release-smoke-openshift-ci.md)
