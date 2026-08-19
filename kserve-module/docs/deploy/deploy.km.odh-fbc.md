# Deploy via Official OpenDataHub FBC (OpenShift)

Installs the ODH operator (which manages the KServe module) from the official
FBC fragment published to `quay.io/opendatahub`. Use this to test against a
released OpenDataHub catalog on an OpenShift cluster.

> **Prerequisite:** an OpenShift cluster and `oc` logged in as cluster-admin.
> The ODH FBC on `quay.io/opendatahub` is public, so no image mirror or
> pull-secret setup is needed (unlike the RHOAI flow).

## Environment Variables

| Variable | Description |
|----------|-------------|
| `CATALOG_IMG` | Official FBC fragment image (digest-pinned) |

```bash
export CATALOG_IMG=quay.io/opendatahub/opendatahub-operator-catalog:odh-stable@sha256:a8e15e72ac425f9c508777e081b9982893570a9792a4b71cdb399c19f4377e24
```

## Step 1 - Create the CatalogSource

```bash
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: odh-operator-test
  namespace: openshift-marketplace
spec:
  displayName: Custom_Catalog
  publisher: odh_fbc
  image: ${CATALOG_IMG}
  sourceType: grpc
EOF
```

## Step 2 - Install the operator

```bash
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
  installPlanApproval: Automatic
EOF
```

## Step 3 - Create DSCI and DSC

```bash
cat <<EOF | oc apply -f -
apiVersion: dscinitialization.opendatahub.io/v2
kind: DSCInitialization
metadata:
  name: default-dsci
  labels:
    app.kubernetes.io/name: dscinitialization
spec:
  applicationsNamespace: opendatahub
  monitoring:
    metrics: {}
    namespace: opendatahub
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
