# Distro-Specific Dependencies

These rules apply to changes in `go.mod` and `*.go` files that add new imports. Skip for
other file types.

This repository is a midstream fork of kserve/kserve. Midstream-only Go source files
(`*_odh.go`) import OCP/ODH-specific packages that do not exist in upstream's
dependency graph. These dependencies must be kept visually separate in `go.mod` and their
imports must live in `//go:build distro` files so that upstream builds stay clean and
upstream syncs stay conflict-free.

## What counts as a distro-specific import

A Go import is distro-specific if its module path matches any of these prefixes:

- `github.com/openshift/` - OpenShift API types and client libraries
- `github.com/opendatahub-io/` - ODH platform libraries
- `github.com/prometheus-operator/` - Prometheus Operator CRD types (standard in OCP, not in vanilla K8s)

### Exemptions

- `istio.io/*` - upstream kserve uses Istio directly; these are NOT distro-specific

When in doubt, check whether upstream `kserve/kserve:master` has the same import. If it
does, the dep is upstream - not distro-specific.

## go.mod structure

`go.mod` uses three `require` blocks in this order:

1. **Upstream direct** - dependencies shared with upstream kserve/kserve.
2. **Indirect** - transitive dependencies (managed by `go mod tidy`).
3. **Distro-specific direct** - dependencies used exclusively by `//go:build distro` files.
   This block carries a comment header explaining its purpose.

```go
// Distro-specific dependencies for opendatahub-io/kserve (OCP platform).
// Used by //go:build distro files only. Do not remove during upstream sync -
// go mod tidy preserves these automatically when distro-tagged files exist.
require (
    github.com/openshift/api ...
    github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring ...
)
```

`go mod tidy` preserves this block, its comment, and its position. Repeated tidy runs
produce identical output.

## Violations - flag as blocking

### go.mod violations

1. **Distro dep in the upstream block** - If a dependency is imported only by
   `//go:build distro` files (check with `go mod why -m <module>`), it must be in the
   distro-specific require block, not the upstream block. Flag and suggest moving it.

2. **Missing comment on distro block** - The distro-specific require block must have the
   comment header above it. If a new distro dep is added to a bare require block, flag it.

3. **Distro block removed during sync** - If a sync PR removes the distro-specific
   require block or its entries, flag as blocking. These deps are required for midstream
   builds and `go mod tidy` will restore them, making the tree dirty.

### Import violations

4. **Distro-specific import in untagged file** - If a Go file imports a distro-specific
   module (matching the prefixes above) and does NOT have `//go:build distro`, flag it.
   The import must live in a `*_odh.go` companion file with `//go:build distro`,
   or the file itself must be tagged. This applies to both production code and test files.
   See `.rules/build-tags.md` for the companion file pattern.

5. **New distro-specific import without go.mod update** - If a PR adds a new
   distro-specific import that introduces a module not yet in go.mod, verify it lands in
   the distro-specific require block (not the upstream block). `go mod tidy` will place
   new deps in whichever block the importer belongs to - if the importer is properly
   tagged, the dep lands in the right block automatically.

## How to verify

```bash
# Check which block a dep belongs to:
go mod why -m github.com/openshift/api
# If the chain goes through a //go:build distro file, the dep belongs in the distro block.

# Verify tidy is stable:
go mod tidy && git diff go.mod
# Should show no changes if go.mod is correctly structured.

# Find distro imports in untagged files (should return empty):
for prefix in "github.com/openshift/" "github.com/opendatahub-io/" "github.com/prometheus-operator/"; do
  grep -rl "\"${prefix}" --include="*.go" pkg/ cmd/ | while read f; do
    # check lines before "package" for a build tag
    awk '/^package /{exit} /go:build distro|\/\/ \+build distro/{found=1} END{exit !found}' "$f" ||
      echo "VIOLATION: $f imports $prefix without build tag"
  done
done
```

## Exemptions - do not flag

- Dependencies imported by both tagged and untagged files belong in the upstream
  block (they are needed regardless of build tags).
- `istio.io/*` dependencies - upstream kserve uses Istio directly.
- Files under `kserve-module/` - separate Go module with its own `go.mod`.
