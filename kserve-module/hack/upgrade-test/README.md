# KServe Module Migration — Upgrade Test Guide

End-to-end guide for verifying service impact when transitioning from the in-tree kserve component to kserve-module-operator.

## Overview

This guide covers two things:

1. **FBC-based operator build/deploy** — setting up the upgrade environment from old (in-tree) to new (module)
2. **Service impact testing** — verifying inference endpoint availability during upgrade + HTML report

## Tool Structure

```
hack/upgrade-test/
├── test-upgrade-service-impact.sh   # Main orchestrator
├── upgrade-diff.sh                  # Cluster state snapshot + diff
├── load-generator.sh                # Continuous health check (runs inside a pod)
├── generate-report.sh               # HTML report generation
└── test-manifests/                  # Test resource definitions
    ├── mlserver-runtime.yaml        # ServingRuntime fallback (when Template is unavailable)
    ├── sklearn-iris-isvc.yaml       # ISVC (sklearn + mlserver)
    ├── sklearn-iris-input.json      # ISVC inference request body
    ├── llmisvc-opt-125m-cpu.yaml    # LLMISVC (vLLM CPU, opt-125m)
    └── llmisvc-input.json           # LLMISVC inference request body
```

All test data is stored in a single **output directory** (referred to as `$WORK` below):

```
$WORK/
├── repos/
│   └── opendatahub-operator/       # cloned repo (old + new branches)
├── snapshots/{pre-upgrade,post-upgrade}/
├── load-test-sklearn-iris.jsonl
├── load-test-llmisvc.jsonl
├── diff-report.txt
└── upgrade-report.html
```

---

## Part 0 — Setup Working Directory

Create a working directory and set environment variables. All subsequent steps run from here.

```bash
export WORK=/tmp/upgrade-test-$(date +%Y%m%d-%H%M%S)
mkdir -p $WORK/repos
cd $WORK

export GIT_NAME=jooho      # Update
export QUAY_NAME=jooholee  # Update
export IMAGE_TAG_BASE=quay.io/${QUAY_NAME}/opendatahub-operator
export VERSION_OLD=3.5.0-ea2
export VERSION_NEW=3.5.0

podman login quay.io
```

Clone the opendatahub-operator repo:

```bash
git clone https://github.com/opendatahub-io/opendatahub-operator.git $WORK/repos/opendatahub-operator
cd $WORK/repos/opendatahub-operator
```

---

## Part 1 — Building FBC Index Images

All build commands run from `$WORK/repos/opendatahub-operator`.

### Step 1: Build old operator (in-tree kserve)

```bash
cd $WORK/repos/opendatahub-operator
git checkout origin/main --detach
make get-manifests

export OPERATOR_IMG_OLD=${IMAGE_TAG_BASE}:v${VERSION_OLD}
export BUNDLE_IMG_OLD=${IMAGE_TAG_BASE}-bundle:v${VERSION_OLD}

make image-build image-push IMG=$OPERATOR_IMG_OLD USE_LOCAL=true

make bundle-build bundle-push \
  IMG=$OPERATOR_IMG_OLD \
  IMG_TAG=v${VERSION_OLD} \
  BUNDLE_IMG=$BUNDLE_IMG_OLD \
  VERSION=${VERSION_OLD}
```

Verify the bundle CSV contains the correct image:

```bash
podman create --name bundle-chk $BUNDLE_IMG_OLD && \
podman cp bundle-chk:/manifests/opendatahub-operator.clusterserviceversion.yaml /tmp/csv-check.yaml && \
podman rm bundle-chk && \
grep "image:.*opendatahub-operator" /tmp/csv-check.yaml
# Output should contain :v${VERSION_OLD} tag. If it shows :latest, IMG was not passed correctly.
```

### Step 2: Build new operator (kserve-module handler)

The new operator source can come from a PR, a fork branch, or a local directory.

**Option A: From a PR**

```bash
PR_NUMBER=3704 #UPDATE
cd $WORK/repos/opendatahub-operator
gh pr checkout $PR_NUMBER --repo opendatahub-io/opendatahub-operator --force
```

**Option B: From a fork repo + branch**

```bash
cd $WORK/repos/opendatahub-operator
git remote add fork https://github.com/${GIT_NAME}/opendatahub-operator.git 2>/dev/null || true
git fetch fork RHOAIENG-61204/kserve-module-handler
git checkout fork/RHOAIENG-61204/kserve-module-handler --detach
```

**Option C: From a local directory**

```bash
# If you already have the source locally, just use that directory instead
cd /path/to/your/local/opendatahub-operator
```

Then build:

```bash
make get-manifests

export OPERATOR_IMG_NEW=${IMAGE_TAG_BASE}:v${VERSION_NEW}
export BUNDLE_IMG_NEW=${IMAGE_TAG_BASE}-bundle:v${VERSION_NEW}

make image-build image-push IMG=$OPERATOR_IMG_NEW USE_LOCAL=true

make bundle-build bundle-push \
  IMG=$OPERATOR_IMG_NEW \
  IMG_TAG=v${VERSION_NEW} \
  BUNDLE_IMG=$BUNDLE_IMG_NEW \
  VERSION=${VERSION_NEW}
```

### Step 3: Build FBC catalog

```bash
cd $WORK/repos/opendatahub-operator

export CATALOG_TAG=$(date +%s)

make catalog-build catalog-push \
  BUNDLE_IMGS=${IMAGE_TAG_BASE}-bundle:v${VERSION_OLD},${IMAGE_TAG_BASE}-bundle:v${VERSION_NEW} \
  CATALOG_IMG=${IMAGE_TAG_BASE}-catalog:${CATALOG_TAG}
```

### Rebuilding the catalog

When you rebuild a bundle and need to update the catalog:

```bash
cd $WORK/repos/opendatahub-operator

export CATALOG_TAG=$(date +%s)
make catalog-build catalog-push \
  BUNDLE_IMGS=${IMAGE_TAG_BASE}-bundle:v${VERSION_OLD},${IMAGE_TAG_BASE}-bundle:v${VERSION_NEW} \
  CATALOG_IMG=${IMAGE_TAG_BASE}-catalog:${CATALOG_TAG}

# Delete existing CSV/Subscription
CSV_NAME=$(oc get csv -n openshift-operators -o name | grep opendatahub-operator)
[ -n "$CSV_NAME" ] && oc delete $CSV_NAME -n openshift-operators
oc delete sub opendatahub-operator -n openshift-operators --ignore-not-found

# Update CatalogSource
oc patch catalogsource odh-operator-test -n openshift-marketplace \
  --type merge -p "{\"spec\":{\"image\":\"${IMAGE_TAG_BASE}-catalog:${CATALOG_TAG}\"}}"

# Recreate Subscription
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: opendatahub-operator
  namespace: openshift-operators
spec:
  channel: fast
  name: opendatahub-operator
  source: odh-operator-test
  sourceNamespace: openshift-marketplace
  startingCSV: opendatahub-operator.v${VERSION_OLD}
  installPlanApproval: Manual
EOF

# Approve InstallPlan
oc wait --for=condition=InstallPlanPending=true \
  Subscription/opendatahub-operator -n openshift-operators --timeout=120s
INSTALL_PLAN=$(oc get subscription -n openshift-operators opendatahub-operator -o jsonpath='{.status.installplan.name}')
oc patch installplan $INSTALL_PLAN -n openshift-operators --type merge --patch '{"spec":{"approved":true}}'
```

---

## Part 2 — Install Old Version + Deploy Services

### 2.1 Create CatalogSource + Subscription

```bash
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: odh-operator-test
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: ${IMAGE_TAG_BASE}-catalog:${CATALOG_TAG}
  displayName: ODH Operator Test
  publisher: Test
  updateStrategy:
    registryPoll:
      interval: 30s
EOF

cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: opendatahub-operator
  namespace: openshift-operators
spec:
  channel: fast
  name: opendatahub-operator
  source: odh-operator-test
  sourceNamespace: openshift-marketplace
  startingCSV: opendatahub-operator.v${VERSION_OLD}
  installPlanApproval: Manual
EOF
```

**Tip: How to override module image by env in subscription**
```
spec:
  channel: fast
  config:
    env:
    - name: RELATED_IMAGE_ODH_KSERVE_MODULE_OPERATOR_IMAGE
      value: quay.io/${QUAY_NAME}/kserve-module-controller:20260716-1
  installPlanApproval: Manual
```

### 2.2 Approve InstallPlan

```bash
oc get installplan -n openshift-operators
INSTALL_PLAN=$(oc get installplan -n openshift-operators -o jsonpath='{.items[?(@.spec.approved==false)].metadata.name}')
oc patch installplan $INSTALL_PLAN -n openshift-operators \
  --type merge -p '{"spec":{"approved":true}}'
```

### 2.3 Create DSCI/DSC

```bash
cat <<EOF | oc apply -f -
apiVersion: dscinitialization.opendatahub.io/v1
kind: DSCInitialization
metadata:
  name: default-dsci
spec:
  applicationsNamespace: opendatahub
  monitoring:
    managementState: Managed
    namespace: opendatahub
EOF

cat <<EOF | oc apply -f -
apiVersion: datasciencecluster.opendatahub.io/v1
kind: DataScienceCluster
metadata:
  name: default-dsc
spec:
  components:
    kserve:
      managementState: Managed
    dashboard:
      managementState: Removed
    workbenches:
      managementState: Removed
    modelmeshserving:
      managementState: Removed
    datasciencepipelines:
      managementState: Removed
    codeflare:
      managementState: Removed
    ray:
      managementState: Removed
    trustyai:
      managementState: Removed
    modelregistry:
      managementState: Removed
    trainingoperator:
      managementState: Removed
    feastoperator:
      managementState: Removed
EOF
```

### 2.4 Deploy test services + start load generator

Run the test scripts from `hack/upgrade-test/` (either from this repo or copied to `$WORK`):

```bash
# Set output dir so all data goes to $WORK
export UPGRADE_TEST_OUTPUT_DIR=$WORK

# ISVC only
./test-upgrade-service-impact.sh setup --mode isvc

# Or ISVC + LLMISVC
./test-upgrade-service-impact.sh setup --mode all

# Start load generator
./test-upgrade-service-impact.sh start-load
```

---

## Part 3 — Run the Upgrade

### 3.1 Pre-upgrade snapshot

```bash
./upgrade-diff.sh snapshot --name pre-upgrade
```

### 3.2 Approve upgrade

```bash
INSTALL_PLAN=$(oc get installplan -n openshift-operators -o jsonpath='{.items[?(@.spec.approved==false)].metadata.name}')
oc patch installplan $INSTALL_PLAN -n openshift-operators \
  --type merge -p '{"spec":{"approved":true}}'

# Wait for upgrade to complete
oc wait --for=jsonpath='{.status.phase}'=Succeeded \
  csv/opendatahub-operator.v${VERSION_NEW} -n openshift-operators --timeout=300s
```

### 3.3 Post-upgrade snapshot + report

```bash
./upgrade-diff.sh snapshot --name post-upgrade
./test-upgrade-service-impact.sh stop-load
./test-upgrade-service-impact.sh report
```

`upgrade-report.html` is generated in `$WORK/`. Open in a browser to review.


---

## opendatahub-test upgrade test

```
cd /tmp
git clone git@github.com:opendatahub-io/opendatahub-tests.git
cd opendatahub-tests


uv run pytest ./tests/model_serving/model_server/upgrade --pre-upgrade \
      --cluster-sanity-skip-rhoai-check \
      --tc=applications_namespace:opendatahub \
      --tc=use_unprivileged_client:False

# please upgrade manually

uv run pytest tests/model_serving/model_server/upgrade --post-upgrade \
    --cluster-sanity-skip-rhoai-check \
    --tc=applications_namespace:opendatahub \
    --tc=use_unprivileged_client:False
```


## Other tests

### For kserve specific tests
```
uv run pytest tests/model_serving/model_server/kserve/ \
    --cluster-sanity-skip-rhoai-check \
    --tc=applications_namespace:opendatahub \
    --tc=use_unprivileged_client:False
```

### For all kserve tests
```
uv run pytest tests/model_serving/model_server/ \
    --cluster-sanity-skip-rhoai-check \
    --tc=applications_namespace:opendatahub \
    --tc=use_unprivileged_client:False
```
