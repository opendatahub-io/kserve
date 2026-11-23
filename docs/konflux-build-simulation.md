# Konflux build simulation

Konflux builds the images in this repository from the PipelineRuns in
`.tekton/`. Until this workflow existed, no pull request check built them:

- `distro-build-check` compiles Go, with and without the `distro` tag, but
  never runs a Dockerfile.
- The `*-docker-publish` workflows are path-filtered to `cmd/`, `pkg/` and
  their own Dockerfile, build for `linux/arm64`, and do not pass
  `GOTAGS=distro`. An image built that way is not the image Konflux ships.

`konflux-build-simulation` generates its matrix from `.tekton/` on every run,
builds each image with the dockerfile, context and build args the PipelineRun
uses, and then starts it.

## What it runs

1. **Target resolution.** `hack/konflux-sim/generate-build-matrix.sh` reads
   every `.tekton/*pull*.yaml`, keeps the ones whose `on-cel-expression`
   selects `pull_request`, and resolves the Dockerfile the way the buildah task
   does: against `path-context` first, then the repository root. A target it
   cannot resolve fails the job; it is never dropped.
2. **Build-arg contract.** Every build-arg the PipelineRun passes must be
   declared with `ARG` in that Dockerfile, and `GOTAGS` must reach
   `go build -tags`. A Dockerfile that silently ignores `GOTAGS=distro` still
   builds, and ships an image with the distro-gated code excluded - that is
   the failure from #1567.
3. **Build.** `docker build` with the resolved dockerfile, context and build
   args, one matrix leg per image, `fail-fast: false`.
4. **Smoke test.** The built image is started:
   - controller-style images: `<entrypoint> --help`, non-zero exit fails;
   - `storage-initializer`: the entrypoint's usage path, which proves the
     interpreter and the `kserve_storage` import load;
   - `autogluon`: `python -c "import autogluonserver"`.
   Every image must also run as a non-root user; the deployed workloads run
   under a restricted SCC and an image that regresses to root still builds.

## What it does not do

- **Multi-architecture.** Most targets inherit `build-platforms: ["linux/x86_64"]`
  from `odh-konflux-central`. `odh-kserve-kueue-controller` asks for four
  architectures and is not built here; the summary lists it with the reason.
- **Hermetic builds.** The kserve PipelineRuns do not set `hermetic` or
  `prefetch-input`, so Konflux builds these with network access. Adding
  `--network=none` here would test a configuration production does not use.
- **Konflux policy tasks.** SAST, the certification preflight and the
  comment-gated `/group-test` pipeline are not reproduced.
- **Operator deployment.** `e2e-test-odh-xks-kind.yml` already deploys the
  module image to a Kind cluster. This workflow does not duplicate it.

## Running it locally

```bash
make yq
YQ=./bin/yq ./hack/konflux-sim/generate-build-matrix.sh | jq .

# Build one target the way CI does.
docker build --build-arg GOTAGS=distro -f Dockerfile -t konflux-sim/kserve-controller:local .
hack/konflux-sim/smoke-test.sh 'help:/manager' konflux-sim/kserve-controller:local
```

## Skipping

Put `[skip konflux-sim]` in the pull request title or add the
`skip-konflux-sim` label. The summary job then reports a skip explicitly; it
never reports success for builds that did not run.
