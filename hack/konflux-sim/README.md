# Konflux build simulation

PR-time CI that reproduces, in GitHub Actions, the failure classes the
**downstream** (red-hat-data-services) Konflux builds hit — because the
midstream `.tekton` PR pipelines are **not hermetic** (odh-konflux-central
`multi-arch-container-build` defaults: `hermetic: "false"`, no
`prefetch-input`, `build-platforms: [linux/x86_64]`), so those failures only
surface days later, in the downstream build or at deploy time.

Workflow: [`.github/workflows/konflux-build-simulation.yml`](../../.github/workflows/konflux-build-simulation.yml)

Skipping: put `[skip konflux-sim]` in the PR title or the `skip-konflux-sim`
label on the PR. The `summary` job ("Konflux build simulation") is the only
check that needs to be required: it fails when any check that ran failed and
passes when checks were correctly skipped.

## Checks

| Script | Job | Failure class it catches |
|---|---|---|
| `hermetic_go_preflight.sh` | Hermetic Go build preflight | a Go module missing from the downstream prefetch cache, or go.mod/go.sum drift, that only fails with the network off (RHOAIENG-89178) |
| `hermetic_python_preflight.sh` | Hermetic Python preflight | a uv.lock / pip spec that cannot be satisfied from the prefetch cache offline (RHOAIENG-13043, RHOAIENG-12260, RHOAIENG-13042/28024) |
| — (matrix in the workflow) | Image build | a Dockerfile / build-args change that breaks the image build itself (RHOAIENG-95421, RHOAIENG-46766), and build-args parity with `.tekton` (RHOAIENG-53489: the distro tag dropped downstream) |
| `rendered_manifest_checks.sh` | Rendered manifest checks | a RoleBinding subject with no ServiceAccount in the applied union (feast-module RHOAIENG-100514/100515), a pod running as a ServiceAccount nothing creates, a duplicated env name the API server silently resolves to the last entry (RHOAIENG-57579) |
| `manifest_path_contract.sh` | Rendered manifest checks | the image, the operator constants and the Deployment mount disagreeing on where the shipped manifests live (model-registry RHOAIENG-99568 shape) |
| `related_image_availability.sh` | Rendered image availability | a rendered image param pointing at a repo:tag that was never published — ImagePullBackOff at deploy time (RHOAIENG-88996, RHOAIENG-69531) |

What the image-build matrix builds is exactly what the `.tekton` pull
PipelineRuns build — same dockerfile, path-context and build-args for all 10
images. Two deliberate deviations, both noted in the workflow:

- `odh-kserve-kueue-controller`: only the `builder` stage is built (the final
  stage `FROM registry.redhat.io/...` is auth-gated; the Konflux tenant has
  Red Hat registry credentials, this workflow does not), and only
  `linux/amd64` (Konflux builds 4 platforms for this image).
- The builds are not hermetic — neither is the midstream Konflux pipeline.
  The hermetic dimension is covered by the Go and Python preflights.

## Local runs

```bash
# Go: <module-dir> <cmd-dir>|<binary>|<tags>...
./hack/konflux-sim/hermetic_go_preflight.sh . 'cmd/manager|manager|distro'
./hack/konflux-sim/hermetic_go_preflight.sh kserve-module 'cmd/kserve-module|module-manager|'

# Python: <project-dir> [unlocked pip specs the Dockerfile installs]
UV_PYTHON=3.11 UV_SYNC_ARGS="--extra confidential" \
  ./hack/konflux-sim/hermetic_python_preflight.sh python/storage \
  krbcontext==0.10 'hdfs~=2.6.0' requests-kerberos==0.14.0

# Rendered manifest checks (KUSTOMIZE=bin/kustomize if you use make-managed tools)
KUSTOMIZE=bin/kustomize ./hack/konflux-sim/rendered_manifest_checks.sh
./hack/konflux-sim/manifest_path_contract.sh

# Image availability (CONTAINER_TOOL=podman or docker)
CONTAINER_TOOL=podman ./hack/konflux-sim/related_image_availability.sh
```

Caveats on macOS: the Python preflight runs only its warm phase there (the
uv cache is keyed per-platform, so an offline proof on darwin would not
prove what the linux image needs — the script says so instead of passing
vacuously); the Go preflight cross-compiles with `GOOS=linux`, which is
still a full offline link proof.

`rendered_manifest_checks.sh` also accepts pre-rendered files as arguments,
which is how fault injection is done: render once, tamper with the copy,
re-run the checks against it.

## Known-stale findings on a clean HEAD

- `config/overlays/odh/rbac/tls/clusterrolebinding.yaml` still lists
  `kserve/kserve-localmodel-controller-manager` and
  `kserve/kserve-localmodelnode-agent` as subjects, but the main ODH overlay
  never creates those ServiceAccounts (the modelcache overlay ships its own
  correct binding in namespace `model-serving-cache`). The entries are dead
  but harmless — permissions granted to identities that never exist — so
  they are reported as warnings, not failures, until cleaned up.

## Fault-injection record

Each check was proven to fire on a tampered copy of a clean tree; everything
was reverted afterwards.

| Check | Injection | Result |
|---|---|---|
| Go preflight (offline phase) | `k8s.io/apimachinery` removed from the isolated module cache between the network and offline phases — the prefetch-gap shape | offline build fails with `module lookup disabled by GOPROXY=off`, exit 1 |
| Python preflight | pip spec `fault-injection-missing-from-prefetch==1.0.0` that no registry has | uv resolution fails, exit 1 |
| RBAC subjects | rendered subject renamed to `fault-injection-does-not-exist` | `FAIL: rendered manifest checks found 1 problem(s)`, exit 1 |
| Duplicate env | `POD_NAMESPACE` duplicated on the module manager container | `FAIL: ... env name defined more than once`, exit 1 |
| Manifest path contract | `manifestsTemplatePath` in `kserve-module/cmd/kserve-module/main.go` renamed to `/opt/aihub-manifests` | `FAIL: manifest path contract broken`, exit 1 |
| Image availability | `kserve-router` param pointed at `quay.io/opendatahub/fault-injection-never-published:odh-v99.0` | `FAIL: some rendered images do not exist`, exit 1 |

## Not covered here (and why)

- **Generated-artifact drift** — already covered by
  [`precommit-check.yml`](../../.github/workflows/precommit-check.yml)
  (`make check` / `make precommit`) on every PR; duplicating it here would
  only add runtime.
- **Downstream konflux-central prefetch-input config** (RHOAIENG-88284/88285/
  88286/87548/87549) — lives in another repo; the hermetic preflights are the
  midstream-side simulation of the consequence.
- **localmodelnode-agent shadow-utils RPM prefetch drift** (RHOAIENG-89178,
  the RPM half) — the drift lives in red-hat-data-services; the Dockerfile
  side is covered by the image-build matrix.
- **Multi-arch** (RHOAIENG-50322, s390x/ppc64le `hf_xet` maturin build) —
  the midstream Konflux pipeline builds `linux/x86_64` only (kueue excepted);
  the simulation builds amd64. A multi-arch failure is not observable at
  this layer.
- **Quay auth / registry outages** (RHOAIENG-24205) — infrastructure, not
  reviewable state.
- **Operator uninstall/finalizer behavior** (RHOAIENG-96233) and
  **LLMISVC runtime 503s** (RHOAIENG-55154) — runtime behavior, covered by
  the operator e2e (opendatahub-operator registers the `kserve` test suite
  and runs it in `e2e-test-xks`; no platform-operator follow-up needed).
