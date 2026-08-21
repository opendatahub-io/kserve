# Deploy via Official RHOAI FBC (OpenShift)

Installs the RHOAI operator (which manages the KServe module) from the official
FBC fragment published to `quay.io/rhoai`. Use this to test against a released
RHOAI catalog on an OpenShift cluster.

> **Prerequisite:** an OpenShift cluster, `oc` logged in as cluster-admin, and a
> `quay.io` account with access to `quay.io/rhoai`.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `CATALOG_IMG` | Official FBC fragment image (digest-pinned) |
| `PERSONAL_PULL_SECRET_FILE` | Local docker config holding your `quay.io` credentials |

```bash
export CATALOG_IMG=quay.io/rhoai/rhoai-fbc-fragment:rhoai-3.5@sha256:5a4db4afc05c4284dc6108edb0d47c8fbe06015cd29eef8095a10b3fb3f58b46
export PERSONAL_PULL_SECRET_FILE="$HOME/.docker/config.json"
```

## Step 1 - Mirror registry.redhat.io/rhoai to quay.io/rhoai

The FBC fragment references images under `registry.redhat.io/rhoai`. Redirect
those pulls to `quay.io/rhoai` with an `ImageDigestMirrorSet`.

```bash
cat <<EOF | oc apply -f -
apiVersion: config.openshift.io/v1
kind: ImageDigestMirrorSet
metadata:
  name: rhods-mirror-policy
spec:
  imageDigestMirrors:
  - source: registry.redhat.io/rhoai
    mirrors:
    - quay.io/rhoai
  - source: registry.redhat.io/redhat-operator-index
    mirrors:
    - quay.io/rhoai
EOF
```

## Step 2 - Add quay.io/rhoai pull credentials

Verify locally first, then add a repo-scoped credential to the cluster pull-secret.

```bash
# 0) Verify locally first. If you can't pull it, the cluster can't either.
#    A successful pull confirms your quay.io account has access to quay.io/rhoai.
docker login quay.io
docker pull $CATALOG_IMG

# 1) Export the current cluster pull-secret
oc get secret pull-secret -n openshift-config \
  -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d > /tmp/pull-secret.json

# 2) Add a repo-scoped credential for quay.io/rhoai.
#    This adds only the quay.io/rhoai key and does NOT overwrite the host-level
#    quay.io entry (CRI-O prefers the more specific namespace key).
jq --slurpfile personal "$PERSONAL_PULL_SECRET_FILE" \
  '.auths["quay.io/rhoai"] = $personal[0].auths["quay.io"]' \
  /tmp/pull-secret.json > /tmp/final-pull-secret.json

# 3) Apply back. NOTE: this triggers a rolling MCO reboot of all nodes.
oc set data secret/pull-secret -n openshift-config \
  --from-file=.dockerconfigjson=/tmp/final-pull-secret.json
```

## Step 3 - Create the CatalogSource

```bash
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: rhoai-catalog-dev
  namespace: openshift-marketplace
spec:
  displayName: Custom_Catalog
  publisher: redhat_fbc
  image: ${CATALOG_IMG}
  sourceType: grpc
EOF
```

## Step 4 - Install the operator

```bash
oc new-project redhat-ods-operator

cat <<EOF | oc apply -f -
kind: OperatorGroup
apiVersion: operators.coreos.com/v1
metadata:
  name: redhat-ods-operator
  namespace: redhat-ods-operator
spec:
  upgradeStrategy: Default
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: test-rhoai-operator
  namespace: redhat-ods-operator
spec:
  channel: beta
  name: rhods-operator
  source: rhoai-catalog-dev
  sourceNamespace: openshift-marketplace
  startingCSV: rhods-operator.3.5.0
  installPlanApproval: Automatic
EOF
```

## Step 5 - Create DSCI and DSC

```bash
cat <<EOF | oc apply -f -
apiVersion: dscinitialization.opendatahub.io/v2
kind: DSCInitialization
metadata:
  name: default-dsci
  labels:
    app.kubernetes.io/name: dscinitialization
spec:
  applicationsNamespace: redhat-ods-applications
  monitoring:
    metrics: {}
    namespace: redhat-ods-monitoring
    managementState: Managed
  trustedCABundle:
    customCABundle: ''
    managementState: Managed
---
apiVersion: datasciencecluster.opendatahub.io/v1
kind: DataScienceCluster
metadata:
  name: default-dsc
spec:
  components:
    kserve:
      managementState: Managed
EOF
```
