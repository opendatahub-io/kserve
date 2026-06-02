# Midstream RBAC Isolation Rules

Violations 1-3 apply to **Go source files** (`*.go`, `*_test.go`) - skip them for non-Go diffs.
Rule 4 (generated manifest drift) applies to **YAML manifests and Helm charts** - always check
those regardless of whether Go files are in the diff.

## Context - why RBAC markers need package-level isolation

`controller-gen` scans `//+kubebuilder:rbac:` markers at the **package level** and generates
ClusterRole manifests from them. If a midstream-specific marker (e.g. for `route.openshift.io`
or `infrastructure.opendatahub.io`) lives in the same package as upstream controller code,
`controller-gen` will include it in the upstream-generated `role.yaml` - polluting it with
permissions the upstream project does not need.

The fix is a dedicated `distro/` sub-package with its own `controller-gen` invocation
(in `Makefile.overrides.mk`). This generates a **separate** ClusterRole deployed only via the
ODH overlay. The upstream `role.yaml` stays untouched.

**Important**: a `//go:build distro` tag on a file does NOT prevent `controller-gen` from
scanning its RBAC markers. Build tags control Go compilation; `controller-gen` parses all Go
files regardless of build constraints. Only package-level separation (the `distro/` sub-package)
keeps midstream markers out of upstream manifests.

## Violations - flag as blocking

1. **ODH RBAC markers outside `distro/`** - If a file that is NOT under a `distro/`
   package path contains `//+kubebuilder:rbac:` markers referencing `*.opendatahub.io` or
   `*.openshift.io` groups, flag it. These markers pollute the upstream
   `role.yaml` with OCP-specific permissions. Move them to
   `pkg/<controller>/distro/controller_rbac_odh.go`.

   **Common mistake**: placing RBAC markers in a `*_odh.go` file that has
   `//go:build distro` but is NOT under a `distro/` sub-package. The build tag does not help
   here - `controller-gen` ignores build tags. The markers still end up in the upstream
   `role.yaml`. The file must be under a `distro/` package path for isolation to work.

2. **ODH RBAC markers without build tag outside `distro/`** - A stricter form of
   violation #1: same markers as above, additionally in a file without a `//go:build distro` header
   and not in a `distro/` path. If this applies, flag #1 as well. The absence of a build tag means
   the OCP-specific code is compiled unconditionally into the upstream build, making this more
   urgent than #1 alone. The fix is the same: move markers to `distro/` and add the build tag to
   any remaining OCP logic.

## Advisory - flag as non-blocking comment

3. **Istio RBAC markers outside `distro/`** - If a file outside a `distro/` path contains
   `//+kubebuilder:rbac:` markers referencing `*.istio.io` groups, leave a non-blocking comment
   asking whether this permission is OCP/midstream-specific. If it is, it should move to `distro/`.
   If it is genuinely needed by upstream kserve (istio is used upstream too), no action is needed.

   Note: this advisory treatment is intentionally more lenient than the import rule in
   `build-tags.md`, which requires `//go:build distro` for any `istio.io/` import. The reason:
   an import is a compilation artifact (isolate it to avoid upstream build failures), but an RBAC
   permission for `networking.istio.io` may be legitimately needed by upstream kserve regardless of
   where the import lives. If the RBAC marker is midstream-only, move it to `distro/`; if it is
   upstream-needed, leave it and the import rule still applies to the import itself.

## Downstream signal - generated manifest drift

4. If the diff modifies generated RBAC manifests (`config/rbac/role.yaml`,
   `config/rbac/llmisvc/role.yaml`, `charts/*/resources.yaml`) and the changes add
   `opendatahub.io` or `openshift.io` API group entries, this is almost always a symptom of RBAC
   markers placed outside `distro/`. Flag the manifest change and ask the author to check whether
   the source `//+kubebuilder:rbac:` markers are in the correct `distro/` sub-package. If the
   markers are correctly placed, the upstream `role.yaml` should not contain these entries.

## Exemptions - do not flag

- Files under a `distro/` sub-package that contain **only** a `package` declaration and
  `//+kubebuilder:rbac:` marker comments - no function definitions, no type definitions, no imports.
  No build tag is intentional - `controller-gen` must parse them regardless of build configuration,
  and since the file contains no executable code, there is nothing for a build constraint to exclude.
  The Go compiler parses and compiles this file in all configurations (it is just nearly empty);
  `controller-gen` scans it unconditionally to extract the RBAC markers - that is why no build tag
  is needed or wanted.
  This is the canonical correct pattern; see `pkg/controller/v1alpha2/llmisvc/distro/controller_rbac_odh.go`
  as a reference.

## Pre-existing technical debt - do not flag on unmodified files

The following files carry OCP-group RBAC markers predating this policy. Flag only if new markers
are **added** in this PR:

- `pkg/controller/v1beta1/inferenceservice/controller.go` - `networking.istio.io` markers
  remain because they are upstream (exist in `kserve/kserve:master`). Not OCP-specific.
