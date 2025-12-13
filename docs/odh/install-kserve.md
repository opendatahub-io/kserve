# Install ODH KServe on OpenShift

## Prerequisites

You should install dependencies per each case:

- **InferenceService (RawDeployment)**
  - It does not need any dependencies

- **LLMInferenceService**
  - Refer to [this doc](../samples/llmisvc/ocp-setup-for-GA/README.md)

## Installation Methods

### Method 1: Using OpenDataHub Operator (2.2x)

Install the OpenDataHub operator and then deploy KServe using DSCI and DSC.

#### Step 1: Install OpenDataHub Operator

```bash
cat <<EOF | kubectl create -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  labels:
    model.serving.test: "true"
    operators.coreos.com/opendatahub-operator.openshift-operators: ""
  name: test-opendatahub-operator
  namespace: openshift-operators
spec:
  channel: fast
  installPlanApproval: Automatic
  name: opendatahub-operator
  source: community-operators
  sourceNamespace: openshift-marketplace
EOF

until kubectl get crd dscinitializations.dscinitialization.opendatahub.io &> /dev/null; do
  echo "⏳ waiting for CRD to appear…"
  sleep 2
done

kubectl wait pod -l name=opendatahub-operator -n openshift-operators --for=condition=Ready --timeout=120s
```

#### Step 2: Create DSCI and DSC

```bash
cat <<EOF | kubectl create -f -
apiVersion: dscinitialization.opendatahub.io/v1
kind: DSCInitialization
metadata:  
  labels:
    app.kubernetes.io/created-by: opendatahub-operator
    app.kubernetes.io/instance: default
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: dscinitialization
    app.kubernetes.io/part-of: opendatahub-operator
  name: default-dsci  
spec:
  applicationsNamespace: opendatahub
  monitoring:
    managementState: Removed
    namespace: redhat-ods-monitoring
  serviceMesh:
    auth:
      audiences:
      - https://kubernetes.default.svc
    controlPlane:
      metricsCollection: Istio
      name: data-science-smcp
      namespace: istio-system
    managementState: Removed
  trustedCABundle:
    customCABundle: ""
    managementState: Managed
---
apiVersion: datasciencecluster.opendatahub.io/v1
kind: DataScienceCluster
metadata:
  labels:
    app.kubernetes.io/created-by: rhods-operator
    app.kubernetes.io/instance: rhods
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/name: datasciencecluster
    app.kubernetes.io/part-of: rhods-operator
  name: default-dsc
spec:
  components:
    codeflare:
      managementState: Removed
    dashboard:
      managementState: Removed
    datasciencepipelines:
      managementState: Removed
    feastoperator: {}
    kserve:
      defaultDeploymentMode: RawDeployment
      managementState: Managed
      nim:
        managementState: Managed
      rawDeploymentServiceConfig: Headless
      serving:
        ingressGateway:
          certificate:
            type: OpenshiftDefaultIngress
        managementState: Removed
        name: knative-serving
    kueue:
      defaultClusterQueueName: default
      defaultLocalQueueName: default
      managementState: Removed
    llamastackoperator: {}
    modelmeshserving:
      managementState: Removed
    modelregistry:
      managementState: Removed
      registriesNamespace: odh-model-registries
    ray:
      managementState: Removed
    trainingoperator:
      managementState: Removed
    trustyai:
      managementState: Removed
    workbenches:
      managementState: Removed
      workbenchNamespace: opendatahub    
EOF

kubectl wait --for=condition=Established --timeout=60s crd/llminferenceserviceconfigs.serving.kserve.io
kubectl wait --for=condition=ready pod -l control-plane=kserve-controller-manager -n opendatahub --timeout=300s
```

### Method 2: Using Kustomize

Install KServe directly using the kustomize manifests from the KServe repository.

#### Step 1: Install KServe CRDs and Controller

```bash
kubectl create ns opendatahub || true

kubectl kustomize config/crd/ | kubectl apply --server-side=true -f -
until kubectl get crd llminferenceserviceconfigs.serving.kserve.io &> /dev/null; do
  echo "⏳ waiting for CRD to appear…"
  sleep 2
done

kubectl wait --for=condition=Established --timeout=60s crd/llminferenceserviceconfigs.serving.kserve.io

# Use Kustomize 5.7+
kustomize build config/overlays/odh | kubectl apply --server-side=true --force-conflicts -f -

kubectl wait --for=condition=ready pod -l control-plane=kserve-controller-manager -n opendatahub --timeout=300s
```

#### Step 2: Install ODH-MODEL-CONTROLLER (incubating)

```bash
kustomize build test/scripts/openshift-ci | oc apply -n opendatahub -f -
```
