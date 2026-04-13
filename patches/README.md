# Carry-over patches

This directory holds midstream-specific changes that cannot be expressed as
build-tag companion files or Kustomize overlays - typically because the target
is generated, a one-liner buried in a shared function, or a config value that
upstream intentionally keeps different.

Each patch applies cleanly on top of the upstream `kserve/kserve` master and
represents a conscious, documented decision to diverge.

## Applying

```bash
git apply patches/*.patch
```

Order matters when patches depend on each other. The numeric prefix enforces it.

## Adding a patch

1. Make your change and commit it on top of a clean upstream base.
2. Extract it: `git format-patch -1 HEAD -o patches/`
3. Rename to `NNNN-short-description.patch` (next number in sequence).
4. Verify it applies cleanly: `git apply --check patches/NNNN-*.patch`
5. Commit the patch file itself.

## Updating a patch when upstream changes the target

1. Drop the old patch: `git apply -R patches/NNNN-*.patch`
2. Re-apply your change on the new upstream base.
3. Regenerate: `git format-patch -1 HEAD -o patches/` and rename.
4. Commit the updated patch file.

## Patches

| File | What |
|------|------|
| `0001-csr-crd-availability-guard.patch` | Gate `ClusterServingRuntime` watch, webhook registration, and list/get calls on `IsCrdAvailable` — CSR CRD is not installed on OCP/ODH deployments |
