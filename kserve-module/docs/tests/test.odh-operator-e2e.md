# KServe Module - ODH Operator E2E Test

Runs the OpenDataHub operator E2E suite (kserve component) on either an XKS (KinD)
or an OpenShift (OCP) cluster.

> **Note:** These commands run the e2e suite from the
> [opendatahub-operator](https://github.com/opendatahub-io/opendatahub-operator)
> repo, not this one. The platform operator has its own e2e tests that cover the
> kserve component, and the kserve team occasionally needs to run them - so the
> steps are documented here for convenience. Run them from a checkout of that repo.
>
> **Prerequisite:** `operator-sdk` installed (see the
> [operator-sdk install docs](https://sdk.operatorframework.io/docs/installation/)),
> `podman`, and cluster access - `kubectl` for XKS, `oc` for OCP.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `QUAY_NAME` | Your quay.io org/username (push target) |
| `IMAGE_TAG_BASE` | Base image path - `quay.io/${QUAY_NAME}/opendatahub-operator` |
| `VERSION_NEW` | Operator version to build/test |
| `OPERATOR_IMG_NEW` | Operator image - `${IMAGE_TAG_BASE}:v${VERSION_NEW}` |
| `BUNDLE_IMG_NEW` | Bundle image - `${IMAGE_TAG_BASE}-bundle:v${VERSION_NEW}` |

```bash
export QUAY_NAME=<your-quay-username>  # Update
export IMAGE_TAG_BASE=quay.io/${QUAY_NAME}/opendatahub-operator
export VERSION_NEW=3.5.0

export OPERATOR_IMG_NEW=${IMAGE_TAG_BASE}:v${VERSION_NEW}
export BUNDLE_IMG_NEW=${IMAGE_TAG_BASE}-bundle:v${VERSION_NEW}
```

## XKS (KinD)

```bash
# 1. Create KinD cluster
make kind-create
make kind-setup-pull-secrets PULL_SECRET=/path/to/pull-secret.txt

# 2. Build operator image (podman)
make image-build USE_LOCAL=true

# 3. Load image into KinD
make image-kind-load KIND_CLUSTER_NAME=kind-odh

# 4. Create operator namespace
kubectl create namespace opendatahub-operator-system

# 5. Deploy Cloud Manager
make deploy-ccm-local-azure
kubectl rollout status deployment -n opendatahub-cloudmanager-system \
  -l control-plane=controller-manager --timeout=180s

# 6. Deploy AzureKubernetesEngine CR
kubectl apply -f config/cloudmanager/azure/samples/azurekubernetesengine_v1alpha1.yaml
kubectl wait --for=condition=Ready \
  azurekubernetesengine/default-azurekubernetesengine --timeout=300s

# 7. Deploy ODH operator
make deploy-rhaii-local

# 8. Run xKS E2E tests
make e2e-test-xks

# Cleanup
make kind-delete
```

## OCP

```bash
# 1. Create namespace + security label
oc create namespace opendatahub-operator
oc label ns opendatahub-operator \
  security.openshift.io/scc.podSecurityLabelSync=true --overwrite

# 2. Build operator + bundle images
make image-build image-push IMG=$OPERATOR_IMG_NEW USE_LOCAL=true

make bundle-build bundle-push \
  IMG=$OPERATOR_IMG_NEW \
  IMG_TAG=v${VERSION_NEW} \
  BUNDLE_IMG=$BUNDLE_IMG_NEW \
  VERSION=${VERSION_NEW}

# 3. Deploy operator (bundle method)
operator-sdk run bundle --timeout=5m -n opendatahub-operator \
  $BUNDLE_IMG_NEW --verbose

# 4. Wait for operator to be ready
oc wait --for condition=Available --timeout=3m -n opendatahub-operator \
  deployment opendatahub-operator-controller-manager

# 5. Run all E2E tests
unset GOFLAGS
make e2e-test -e E2E_TEST_FLAGS="-timeout 80m" -e OPERATOR_NAMESPACE=opendatahub-operator
```

## E2E Test Variants

Set up the cluster for E2E tests:

```bash
export E2E_TEST_OPERATOR_NAMESPACE=opendatahub-operator
export E2E_TEST_APPLICATIONS_NAMESPACE=opendatahub
make e2e-setup-cluster
```

Run the kserve component suite via Makefile:

```bash
make e2e-test \
  -e E2E_TEST_OPERATOR_NAMESPACE=${E2E_TEST_OPERATOR_NAMESPACE} \
  -e E2E_TEST_COMPONENT=kserve \
  -e E2E_TEST_OPERATOR_CONTROLLER=false \
  -e E2E_TEST_DSC_MANAGEMENT=false \
  -e E2E_TEST_DSC_VALIDATION=false \
  -e E2E_TEST_OPERATOR_RESILIENCE=false \
  -e E2E_TEST_DAG_ORDERING=false \
  -e E2E_TEST_DEPENDANT_OPERATORS_MANAGEMENT=false \
  -e E2E_TEST_SERVICES=false \
  -e E2E_TEST_WEBHOOK=false \
  -e E2E_TEST_OPERATOR_V2TOV3UPGRADE=false \
  -e E2E_TEST_DELETION_POLICY=never \
  -e E2E_TEST_CLEAN_UP_PREVIOUS_RESOURCES=false
```

Or run it directly via `go test`:

```bash
go test ./tests/e2e/ -v -run TestOdhOperator \
  --operator-namespace=opendatahub-operator \
  --test-component=kserve \
  --test-operator-controller=false \
  --test-dsc-management=false \
  --test-dsc-validation=false \
  --test-operator-resilience=false \
  --test-dag-ordering=false \
  --test-dependant-operators-management=false \
  --test-services=false \
  --test-webhook=false \
  --test-operator-v2tov3upgrade=false \
  -timeout 60m
```

## RHOAI Mode

Uses the `redhat-ods-operator` namespace.

```bash
make e2e-test \
  -e ODH_PLATFORM_TYPE=rhoai \
  -e E2E_TEST_FLAGS="-timeout 80m" \
  -e OPERATOR_NAMESPACE=redhat-ods-operator \
  -e E2E_TEST_DSC_MONITORING_NAMESPACE=redhat-ods-monitoring \
  -e E2E_TEST_COMPONENT=kserve \
  -e E2E_TEST_OPERATOR_CONTROLLER=false \
  -e E2E_TEST_DSC_MANAGEMENT=false \
  -e E2E_TEST_DSC_VALIDATION=false \
  -e E2E_TEST_OPERATOR_RESILIENCE=false \
  -e E2E_TEST_DAG_ORDERING=false \
  -e E2E_TEST_DEPENDANT_OPERATORS_MANAGEMENT=false \
  -e E2E_TEST_SERVICES=false \
  -e E2E_TEST_WEBHOOK=false \
  -e E2E_TEST_OPERATOR_V2TOV3UPGRADE=false \
  -e E2E_TEST_DELETION_POLICY=never \
  -e E2E_TEST_CLEAN_UP_PREVIOUS_RESOURCES=false
```

## Cleanup

```bash
operator-sdk cleanup opendatahub-operator -n opendatahub-operator
```
