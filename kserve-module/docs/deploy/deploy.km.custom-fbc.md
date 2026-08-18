# Build OpenDataHub Operator Custom FBC

Builds a custom FBC (File-Based Catalog) for the OpenDataHub operator from your own operator
source (a PR, a fork branch, or a local directory). Deploy the resulting catalog
with the ODH FBC flow.

> **Prerequisite:** `podman` logged in to `quay.io` with push access to your org,
> `gh` CLI (only for the PR checkout option), and a clone of the
> opendatahub-operator repo.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `WORK` | Working directory holding the cloned repo and build artifacts |
| `GIT_NAME` | Your GitHub username (for the fork remote) |
| `QUAY_NAME` | Your quay.io org/username (push target) |
| `IMAGE_TAG_BASE` | Base image path - `quay.io/${QUAY_NAME}/opendatahub-operator` |
| `VERSION_OLD` | Old operator version (only for upgrade testing) |
| `VERSION_NEW` | New operator version |
| `CATALOG_TAG` | Catalog image tag (a timestamp keeps it unique) |

## Step 1 - Set up working directory and clone

All subsequent steps run from `$WORK/repos/opendatahub-operator`.

```bash
export WORK=/tmp/build-odh-operator-$(date +%Y%m%d-%H%M%S)
mkdir -p $WORK/repos

export GIT_NAME=jooho      # Update
export QUAY_NAME=jooholee  # Update
export IMAGE_TAG_BASE=quay.io/${QUAY_NAME}/opendatahub-operator
export VERSION_OLD=3.5.0-ea2
export VERSION_NEW=3.5.0

podman login quay.io

git clone https://github.com/opendatahub-io/opendatahub-operator.git $WORK/repos/opendatahub-operator
cd $WORK/repos/opendatahub-operator
```

## Step 2 - Build old operator (upgrade test only)

Skip this step unless you are building for an upgrade test.

```bash
git checkout origin/main --detach
make get-manifests

export OPERATOR_IMG_OLD=${IMAGE_TAG_BASE}:v${VERSION_OLD} && echo ${OPERATOR_IMG_OLD}
export BUNDLE_IMG_OLD=${IMAGE_TAG_BASE}-bundle:v${VERSION_OLD} && echo ${BUNDLE_IMG_OLD}

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
# If you hit some issues on Mac, try this
# use /private instead /tmp directly
# podman cp bundle-chk:/manifests/opendatahub-operator.clusterserviceversion.yaml /private/tmp/csv-check.yaml

# Output should contain :v${VERSION_OLD} tag. If it shows :latest, IMG was not passed correctly.
#bundle-chk
#                image: quay.io/jooholee/opendatahub-operator:v3.5.0-ea2
#                image: quay.io/jooholee/opendatahub-operator:v3.5.0-ea2
```

## Step 3 - Build new operator (kserve-module handler)

The new operator source can come from a PR, a fork branch, or a local directory.

**Option A: From a PR**

```bash
PR_NUMBER=3704 #UPDATE
gh pr checkout $PR_NUMBER --repo opendatahub-io/opendatahub-operator --force
```

**Option B: From a fork repo + branch**

```bash
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
go run -C ./cmd/manifest-tools main.go download \
    --config $(pwd)/manifests-config.yaml \
    --manifests-dir $(pwd)/opt/manifests \
    --charts-dir $(pwd)/opt/charts \
    --component kserve-module-operator=maskarb:kserve:fix-jwe-full-key-support:kserve-module/config    # You can skip if you reuse downloaded manifests
    # example branch : https://github.com/maskarb/kserve/tree/fix-jwe-full-key-support 

export OPERATOR_IMG_NEW=${IMAGE_TAG_BASE}:v${VERSION_NEW} && echo ${OPERATOR_IMG_NEW}
export BUNDLE_IMG_NEW=${IMAGE_TAG_BASE}-bundle:v${VERSION_NEW} && echo ${BUNDLE_IMG_NEW}

make image-build image-push IMG=$OPERATOR_IMG_NEW USE_LOCAL=true 

make bundle-build bundle-push \
  IMG=$OPERATOR_IMG_NEW \
  IMG_TAG=v${VERSION_NEW} \
  BUNDLE_IMG=$BUNDLE_IMG_NEW \
  VERSION=${VERSION_NEW}
```

> **Manifests:** `make get-manifests` downloads the component manifests (including
> kserve-module's, with their image references) into `./opt/manifests`. To test
> local manifest changes, edit the files under `./opt/manifests` and build with
> `USE_LOCAL=true` — the build reuses those files instead of re-downloading. See
> [opendatahub-operator: Download Manifests](https://github.com/opendatahub-io/opendatahub-operator#download-manifests)
> for more.

## Step 4 - Build the FBC catalog

```bash
cd $WORK/repos/opendatahub-operator

export CATALOG_TAG=$(date +%s)

make catalog-build catalog-push \
  BUNDLE_IMGS=${IMAGE_TAG_BASE}-bundle:v${VERSION_OLD},${IMAGE_TAG_BASE}-bundle:v${VERSION_NEW} \
  CATALOG_IMG=${IMAGE_TAG_BASE}-catalog:${CATALOG_TAG}

# If you hit any issues on Mac, try the following and do it again:
#cat << 'EOF' > ~/.config/containers/policy.json
#{
#  "default": [
#    {
#      "type": "insecureAcceptAnything"
#    }
#  ]
#}
#EOF
```


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

## Step 5 - Rebuild the catalog

When you rebuild a bundle and need to update an already-deployed catalog:

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

To deploy this custom FBC, see [deploy.km.odh-fbc.md](./deploy.km.odh-fbc.md)
(point `CATALOG_IMG` at your custom-built catalog image).
