# Upstream Sync Plan for KServe ODH Fork

## Overview
This plan outlines the strategy to sync 89 commits from upstream KServe to the OpenDataHub (ODH) fork while preserving ODH and OpenShift-specific modifications.

## Current State
- **Upstream remote**: `upstream` (kserve/kserve)
- **Origin remote**: `origin` (opendatahub-io/kserve)
- **Common ancestor**: `865d4f003` (Promote new KServe Storage module #4625)
- **Commits to sync**: 89 commits
- **Date range**: From late 2024 to present

## Critical ODH/OpenShift Modifications to Preserve

### 1. TLS Configuration
- **Default TLS enabled**: ODH enables TLS by default for secure communications
- **Related commits**:
  - `efecad2b7` - Do not register ClusterServingRuntime
  - `2205fda95` - Change TLS certs mount path to not conflict with CA bundles
  - `5f339ff7b` - Support TLS with self-signed certificates for LLMInferenceService
  - `c23a6d612` - RHOAIENG-34916: CA Cert signing and validation

### 2. ClusterServingRuntime vs ServingRuntime
- **ODH uses only ServingRuntime**: ClusterServingRuntime resources are not registered
- **Related commit**: `efecad2b7` - Do not register ClusterServingRuntime
- **Impact**: The controller does not watch or process ClusterServingRuntime resources

### 2a. Istio Networking Permissions in ClusterRoles
- **ODH-specific RBAC**: ClusterRoles have explicit permissions for Istio networking resources
- **Locations**:
  - `config/rbac/role.yaml` lines 123-142
  - `charts/kserve-resources/templates/clusterrole.yaml` lines 136-155
  - `config/rbac/llmisvc/role.yaml` line 96
- **Resources to preserve**:
  - `networking.istio.io/virtualservices` with all verbs (create, delete, get, list, patch, update, watch)
  - `networking.istio.io/virtualservices/finalizers` with all verbs
  - `networking.istio.io/virtualservices/status` with get, patch, update
- **Also preserve**:
  - OpenShift route permissions: `route.openshift.io/routes` and `route.openshift.io/routes/status`
  - Gateway API permissions: `gateway.networking.k8s.io/httproutes`

### 3. Default Deployment Mode
- **ODH default**: RawDeployment mode (not Serverless)
- **Related commits**:
  - `13ee1b5cd` - chore: change default deployment mode to RawDeployment
  - Config in `config/configmap/inferenceservice.yaml` line 713

### 4. Storage Initializer UID Handling
- **OpenShift compatibility**: Special UID handling for OpenShift with istio-cni
- **Related commit**: `4e7f0df84` - add storage-initializer uid handling for OpenShift with istio-cni
- **Config**: `uidModelcar: 1010` in `config/configmap/inferenceservice.yaml` line 632

### 5. OpenShift-specific Manifests
- Network policies for OpenShift
- Removal of manager's rbac-proxy
- Kustomize manifests adapted for OpenShift
- **Related commit**: `9d58b322f` - OpenShift patches to Kustomize manifests

### 6. Security and ServiceAccount Configuration
- `autoMountServiceAccountToken: true` in security config (line 732)
- ServiceClusterIPNone set to true (line 737)

## Sync Strategy

### Adaptive Batch Organization
We will create batches of 12-15 commits each, with the following **critical exception**:

**High-Conflict Commit Strategy**:
If any single commit causes extensive conflicts (affecting 5+ files or requiring significant manual resolution), that commit will be cherry-picked into its own separate PR. This allows for:
- Focused review of complex changes
- Easier conflict resolution
- Better tracking of what upstream changes required adaptation
- Ability to defer or rework problematic commits without blocking the entire sync

**Expected PR count**: 7-10 PRs total (6-7 batches of 12-15 commits + 2-3 individual high-conflict commits)

#### Batch 1: Commits 1-13 (Base fixes and initial features)
**Branch**: `sync-upstream-batch1-YYYYMMDD`
**Commits**: `b72c993b0` to `fcf8e3e3b`
- Focus: HTML fixes, progressive rollout, KEDA autoscaling, security fixes
- Key commits:
  - Progressive rollout for raw deployment
  - Fix HF Token Vulnerability
  - Remove EnableDirectPvcVolumeMount flag

#### Batch 2: Commits 14-26 (Bug fixes and LLMISVC improvements)
**Branch**: `sync-upstream-batch2-YYYYMMDD`
**Commits**: `39abe9ec9` to `dcd81de2f`
- Focus: Autoscaling, LLMISVC features, documentation
- Key commits:
  - Fix autoscaling tests
  - LLMISVC RBAC and templating
  - Time Series Forecast API

#### Batch 3: Commits 27-39 (Storage and configuration enhancements)
**Branch**: `sync-upstream-batch3-YYYYMMDD`
**Commits**: `f54248dad` to `b4ce7e059`
- Focus: Storage improvements, SSL configuration, multiple storage URIs
- Key commits:
  - Support Multiple Storage URIs (CRITICAL - may need adaptation)
  - CA Bundle injection for S3 storage
  - Kueue metadata propagation

#### Batch 4: Commits 40-52 (Storage module and metrics)
**Branch**: `sync-upstream-batch4-YYYYMMDD`
**Commits**: `387acca48` to `da4b146dc`
- Focus: Storage module updates, logging features, controller improvements
- Key commits:
  - Support inference logging to GCS and Azure
  - Expose uidModelcar in helm chart (CRITICAL - relates to ODH uid config)
  - Refactor llmisvc manifests

#### Batch 5: Commits 53-65 (K8s version bump and storage features)
**Branch**: `sync-upstream-batch5-YYYYMMDD`
**Commits**: `2bb561780` to `d5a3f7488`
- Focus: Kubernetes package version bump, storage parallelization
- Key commits:
  - Bump k8s package versions to v0.34.0 (CRITICAL - may break builds)
  - Parallelize blob downloads from S3 and Azure
  - Path traversal fix (CRITICAL SECURITY FIX)

#### Batch 6: Commits 66-78 (Installation scripts and routing)
**Branch**: `sync-upstream-batch6-YYYYMMDD`
**Commits**: `fed48e6ed` to `51f24ac5c`
- Focus: Installation improvements, path templates, storage fixes
- Key commits:
  - Prevent path traversal on https.go (duplicate security fix)
  - Add pathTemplate configuration for routing
  - Fix multiple storage uri volume mount error

#### Batch 7 (Final): Commits 79-89 (CVE fixes and final updates)
**Branch**: `sync-upstream-batch7-YYYYMMDD`
**Commits**: `13a32e86f` to `98db66141`
- Focus: Security CVEs, version updates, final improvements
- Key commits:
  - Pin starlette to fix CVE-2025-62727
  - Update go version to 1.25 and kubebuilder to 1.9.0
  - Address multiple CVEs (2025-22872, 2025-47914, 2025-58181)
  - Update lightgbm for CVE-2024-43598

## Cherry-Pick Process for Each Batch

### Step 1: Create Branch
```bash
# Fetch latest from both remotes
git fetch upstream
git fetch origin

# Create branch from origin/master
git checkout -b sync-upstream-batch<N>-$(date +%Y%m%d) origin/master
```

### Step 2: Cherry-Pick Commits
```bash
# Cherry-pick the range of commits for the batch
# Example for Batch 1:
git cherry-pick b72c993b0^..fcf8e3e3b

# If conflicts occur on a single commit:
# 1. Review the conflict carefully
# 2. Count affected files: git status | grep "both modified" | wc -l
# 3. If 5+ files have conflicts OR conflicts are complex:
#    a. Abort the current cherry-pick: git cherry-pick --abort
#    b. Cherry-pick commits before the problematic one
#    c. Skip the problematic commit for now
#    d. Continue with remaining commits
#    e. Create a separate PR for the problematic commit later
# 4. If conflicts are manageable (< 5 files):
#    a. Check if it's related to ODH-specific modifications
#    b. Preserve ODH modifications while applying upstream changes
#    c. For TLS, ServingRuntime, deployment mode, and RBAC conflicts - always preserve ODH config
git status
git diff
# Fix conflicts maintaining ODH requirements
git add .
git cherry-pick --continue

# After completing the batch, document any skipped commits
echo "Skipped commits (will be handled in separate PRs):" > skipped_commits.txt
echo "<commit_sha> - <reason>" >> skipped_commits.txt
```

### Step 3: Verify ODH-Specific Configurations
After cherry-picking, verify these configurations remain intact:

```bash
# Check deployment mode is still RawDeployment
grep -A 2 "defaultDeploymentMode" config/configmap/inferenceservice.yaml

# Check uidModelcar is still 1010
grep "uidModelcar" config/configmap/inferenceservice.yaml

# Check ClusterServingRuntime is not registered
git log --all --grep="ClusterServingRuntime" -1

# Check TLS configuration
grep -r "TLS\|tls" config/

# CRITICAL: Check Istio networking permissions in ClusterRoles
echo "Checking Istio permissions in config/rbac/role.yaml:"
grep -A 20 "networking.istio.io" config/rbac/role.yaml

echo "Checking Istio permissions in charts/kserve-resources/templates/clusterrole.yaml:"
grep -A 20 "networking.istio.io" charts/kserve-resources/templates/clusterrole.yaml

echo "Checking Istio permissions in config/rbac/llmisvc/role.yaml:"
grep -A 20 "networking.istio.io" config/rbac/llmisvc/role.yaml

# Verify expected permissions exist:
# - networking.istio.io/virtualservices (all verbs)
# - networking.istio.io/virtualservices/finalizers (all verbs)
# - networking.istio.io/virtualservices/status (get, patch, update)
```

### Step 4: Build and Basic Smoke Test
```bash
# Build the controller
make build

# Run unit tests
make test

# Note: Tests may fail - this is expected for intermediate batches
# Document failures in the PR description
```

### Step 5: Push and Create PR
```bash
# Push the branch
git push origin sync-upstream-batch<N>-$(date +%Y%m%d)

# Create PR using gh CLI
gh pr create \
  --title "[Sync Upstream] Batch <N>: <Short Description>" \
  --body "$(cat <<'EOF'
## Summary
Cherry-picked commits <first_commit_num>-<last_commit_num> from upstream kserve/kserve.

## Commit Range
- First: <first_commit_sha> - <title>
- Last: <last_commit_sha> - <title>
- Total commits: <N>

## Key Changes
- <bullet point summary>

## ODH/OpenShift Modifications Preserved
- [x] TLS enabled by default
- [x] Only ServingRuntime (no ClusterServingRuntime)
- [x] RawDeployment as default mode
- [x] uidModelcar set to 1010
- [x] OpenShift-specific manifests intact
- [x] Istio networking permissions in ClusterRoles (networking.istio.io/virtualservices)
- [x] OpenShift route permissions (route.openshift.io/routes)

## Known Issues
<List any conflicts resolved and test failures>

## Testing Status
- [ ] Build: Pass/Fail
- [ ] Unit Tests: Expected failures documented below
- [ ] E2E Tests: Deferred to final PR

## Notes
This is batch <N> of 7. Test failures are expected and will be addressed in the final PR.

🤖 Part of upstream sync effort tracked in <link to epic/issue>

EOF
)" \
  --base master \
  --draft
```

## Conflict Resolution Guidelines

### Expected Conflict Areas

1. **config/configmap/inferenceservice.yaml**
   - **Action**: Always preserve ODH settings (deployment mode, uid, TLS)
   - **Resolution**: Take ODH version for configuration values, upstream version for new features
   - **High-conflict potential**: MEDIUM - config changes are common

2. **pkg/controller/v1beta1/inferenceservice/controller.go**
   - **Action**: Preserve ClusterServingRuntime exclusion logic
   - **Resolution**: Merge carefully, keep ODH's controller logic that skips ClusterServingRuntime
   - **High-conflict potential**: HIGH - controller changes may require separate PR

3. **pkg/apis/serving/v1alpha1/servingruntime_types.go**
   - **Action**: Preserve any ODH-specific type modifications
   - **Resolution**: Review type changes carefully, ensure ClusterServingRuntime is not reintroduced
   - **High-conflict potential**: MEDIUM - type changes are structural

4. **TLS/Security related files**
   - **Action**: Preserve ODH's TLS-by-default configuration
   - **Resolution**: Merge upstream TLS improvements with ODH's defaults
   - **High-conflict potential**: LOW-MEDIUM

5. **Storage initializer files**
   - **Action**: Preserve UID handling for OpenShift
   - **Resolution**: Keep ODH's uid configuration while adopting upstream storage improvements
   - **High-conflict potential**: MEDIUM

6. **ClusterRole RBAC files** (CRITICAL)
   - **Files**:
     - `config/rbac/role.yaml`
     - `charts/kserve-resources/templates/clusterrole.yaml`
     - `config/rbac/llmisvc/role.yaml`
   - **Action**: ALWAYS preserve Istio networking permissions
   - **ODH-specific permissions to preserve**:
     - `networking.istio.io/virtualservices` (all verbs)
     - `networking.istio.io/virtualservices/finalizers` (all verbs)
     - `networking.istio.io/virtualservices/status` (get, patch, update)
     - `route.openshift.io/routes` and status (OpenShift routes)
     - `gateway.networking.k8s.io/httproutes` (Gateway API)
   - **Resolution**: If upstream adds new permissions, merge them in. If upstream removes permissions, KEEP ODH permissions regardless
   - **High-conflict potential**: HIGH - RBAC changes common, if this conflicts extensively, create separate PR

7. **config/default/kustomization.yaml** (CRITICAL - BATCH 2 LESSON LEARNED)
   - **Action**: NEVER take upstream version wholesale - ODH does NOT use cert-manager
   - **ODH-specific configuration**:
     - Uses OpenShift's serving certificates, NOT cert-manager
     - All `metadata.annotations.[cert-manager.io/inject-ca-from]` replacements MUST be commented out
     - Patches that reference cert-manager MUST be commented out:
       - `manager_auth_proxy_patch.yaml` (commented)
       - `clusterservingruntime_validatingwebhook_cainjection_patch.yaml` (commented)
       - `localmodel_manager_image_patch.yaml` (commented)
       - `localmodelnode_agent_image_patch.yaml` (commented)
     - ODH uses `svc_webhook_cainjection_patch.yaml` instead
   - **Resolution**:
     1. Take upstream version
     2. Comment out ALL cert-manager.io/inject-ca-from replacements (lines ~151-230)
     3. Restore ODH patch configuration (see master for reference)
     4. Keep new llmisvc configurations from upstream (they work with OpenShift certs)
   - **High-conflict potential**: HIGH - kustomize changes affect deployment
   - **Error if done wrong**: `unable to find field "metadata.annotations.[cert-manager.io/inject-ca-from]" in replacement target`
   - **Reference**: See Batch 2 fix commit for proper resolution

### Conflict Resolution Strategy
```bash
# For each conflict:
git status  # Identify conflicted files

# Count conflicts to determine if this should be a separate PR:
CONFLICT_COUNT=$(git status | grep "both modified" | wc -l)
if [ $CONFLICT_COUNT -ge 5 ]; then
  echo "WARNING: $CONFLICT_COUNT files in conflict - consider separate PR for this commit"
  echo "Current commit: $(git log -1 --oneline CHERRY_PICK_HEAD)"
fi

# For config files (preserving ODH settings):
# 1. Open the file
# 2. Keep ODH-specific values (deployment mode, uids, TLS settings)
# 3. Add new upstream features/options
# 4. Test the merged config

# For RBAC/ClusterRole files (CRITICAL - preserve Istio permissions):
# 1. Open the conflicted RBAC file
# 2. Ensure these ODH permissions are present:
#    - networking.istio.io/virtualservices (create, delete, get, list, patch, update, watch)
#    - networking.istio.io/virtualservices/finalizers (create, delete, get, list, patch, update, watch)
#    - networking.istio.io/virtualservices/status (get, patch, update)
#    - route.openshift.io/routes and routes/status
# 3. Add any new upstream permissions
# 4. NEVER remove ODH Istio/OpenShift permissions even if upstream removed them

# For code files:
# 1. Understand both changes
# 2. Check if upstream change affects ODH-specific behavior
# 3. Merge both changes if compatible
# 4. Prioritize ODH requirements if incompatible

# If a conflict is too complex or affects many files:
git cherry-pick --abort
# Document the problematic commit for separate PR:
echo "<commit_sha> - <reason for separate PR>" >> skipped_commits.txt
# Continue with next commits
```

## PR Merge Strategy

### Batch PRs 1-6
- **Review**: ODH team review focused on:
  - ODH configurations preserved
  - No ClusterServingRuntime reintroduction
  - TLS settings intact
- **Merge**: Squash merge NOT recommended - keep individual commits for easier tracking
- **Approval**: 2 approvals required
- **CI**: Expected to have failures - document in PR
- **Merge order**: Sequential (Batch 1 → 2 → 3 → 4 → 5 → 6)

### Final PR (Batch 7)
- **Purpose**:
  - Complete the sync with final commits
  - Fix any accumulated issues from previous batches
  - Ensure all tests pass
- **Review**: Comprehensive review including:
  - Full test suite must pass
  - E2E tests in RawDeployment mode
  - Security scan clean
  - All ODH requirements verified
- **Approval**: Full team review + QE signoff

## Verification Checklist (For Final PR)

### Build & Tests
- [ ] `make build` succeeds
- [ ] `make test` all unit tests pass
- [ ] `make e2e-test` passes (RawDeployment mode)
- [ ] No new security vulnerabilities introduced

### ODH Requirements
- [ ] Default deployment mode is RawDeployment
- [ ] TLS enabled by default
- [ ] Only ServingRuntime resources (no ClusterServingRuntime)
- [ ] uidModelcar is 1010 for OpenShift compatibility
- [ ] Storage initializer UID handling for OpenShift intact
- [ ] OpenShift-specific network policies present
- [ ] **CRITICAL**: Istio networking permissions in all ClusterRole files:
  - [ ] `config/rbac/role.yaml` has `networking.istio.io/virtualservices` permissions
  - [ ] `charts/kserve-resources/templates/clusterrole.yaml` has `networking.istio.io/virtualservices` permissions
  - [ ] `config/rbac/llmisvc/role.yaml` has `networking.istio.io/virtualservices` permissions
  - [ ] All three files have virtualservices, virtualservices/finalizers, and virtualservices/status
  - [ ] OpenShift route permissions present (`route.openshift.io/routes`)

### Configuration Files
- [ ] `config/configmap/inferenceservice.yaml` has correct ODH defaults
- [ ] `charts/kserve-resources/values.yaml` has ODH configurations
- [ ] OpenShift CRDs and manifests present

### Functionality
- [ ] ServingRuntime resources work correctly
- [ ] TLS connections work with self-signed certs
- [ ] Storage initializer works in OpenShift with istio-cni
- [ ] RawDeployment mode works as expected
- [ ] All CVE fixes applied

## Timeline Recommendation

- **Batch 1-2**: Week 1 (foundation and bug fixes)
- **Batch 3-4**: Week 2 (storage and features)
- **Batch 5-6**: Week 3 (version bumps and routing)
- **Batch 7**: Week 4 (CVE fixes and final integration)
- **Testing & QE**: Week 5-6
- **Final Merge**: Week 6-7

## Rollback Plan

If a batch introduces critical issues:
```bash
# Revert the merge
git revert -m 1 <merge_commit_sha>

# Or reset the branch
git reset --hard origin/master
git push --force origin <branch-name>
```

## Communication Plan

1. **Before starting**: Announce sync plan to team, get alignment
2. **Each batch PR**:
   - Post in team channel
   - Tag relevant reviewers
   - Document known issues
3. **After each merge**: Update team on progress
4. **Final PR**: Full team review meeting before merge

## Notes

- Each PR should be clearly labeled with batch number (or "High-Conflict Individual Commit" for separated commits)
- Use GitHub Projects or Jira to track all PRs (expect 7-10 total with high-conflict commits)
- Document all conflict resolutions in PR descriptions
- Keep PRs in draft until ready for review
- Test locally before pushing to reduce CI usage
- Consider using a shared document to track known issues across batches
- **Maintain a running list of skipped commits** in `skipped_commits.txt` (not committed to repo)
- High-conflict commits (5+ file conflicts or complex RBAC changes) get individual PRs with:
  - Title: `[Sync Upstream] High-Conflict: <commit title>`
  - Clear documentation of why it was separated
  - Extra thorough review of ODH-specific preservation

## Commands Reference

### Fetch and update
```bash
git fetch upstream && git fetch origin
```

### Create batch branch
```bash
git checkout -b sync-upstream-batch<N>-$(date +%Y%m%d) origin/master
```

### Cherry-pick commit range (example for batch 1)
```bash
git cherry-pick b72c993b0^..fcf8e3e3b
```

### View commit range
```bash
git log --oneline --reverse <first_commit>^..<last_commit>
```

### Check ODH configurations
```bash
# Check deployment mode
grep -A 2 "defaultDeploymentMode" config/configmap/inferenceservice.yaml

# Check uidModelcar
grep "uidModelcar" config/configmap/inferenceservice.yaml

# Check for ClusterServingRuntime
git grep -n "ClusterServingRuntime" pkg/controller/

# Check Istio permissions in RBAC files
grep -B 2 -A 15 "networking.istio.io" config/rbac/role.yaml
grep -B 2 -A 15 "networking.istio.io" charts/kserve-resources/templates/clusterrole.yaml
grep -B 2 -A 15 "networking.istio.io" config/rbac/llmisvc/role.yaml
```

### Quick RBAC verification script
```bash
#!/bin/bash
# Save as check_rbac.sh

echo "=== Checking RBAC files for required Istio permissions ==="

check_file() {
  local file=$1
  echo ""
  echo "Checking $file..."

  if grep -q "networking.istio.io" "$file"; then
    echo "✓ networking.istio.io found"

    if grep -A 20 "networking.istio.io" "$file" | grep -q "virtualservices"; then
      echo "  ✓ virtualservices resource found"
    else
      echo "  ✗ WARNING: virtualservices resource NOT found"
    fi

    if grep -A 20 "networking.istio.io" "$file" | grep -q "finalizers"; then
      echo "  ✓ virtualservices/finalizers found"
    else
      echo "  ✗ WARNING: virtualservices/finalizers NOT found"
    fi

    if grep -A 20 "networking.istio.io" "$file" | grep -q "status"; then
      echo "  ✓ virtualservices/status permissions found"
    else
      echo "  ✗ WARNING: virtualservices/status NOT found"
    fi
  else
    echo "✗ CRITICAL: networking.istio.io NOT found in $file"
  fi
}

check_file "config/rbac/role.yaml"
check_file "charts/kserve-resources/templates/clusterrole.yaml"
check_file "config/rbac/llmisvc/role.yaml"

echo ""
echo "=== Checking for OpenShift route permissions ==="
if grep -q "route.openshift.io" "charts/kserve-resources/templates/clusterrole.yaml"; then
  echo "✓ OpenShift route permissions found"
else
  echo "✗ WARNING: OpenShift route permissions NOT found"
fi
```
