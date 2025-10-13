# OpenShift 4.18 prerequisites for LLM Inference Service (llm-d.ai)

> **⚠️ Disclaimer**: This configuration is not officially supported and is provided for experimental/development
> purposes only.

This directory contains the configuration needed to set up the required infrastructure for running LLM Inference
Services on OpenShift 4.18.

## Deployment Steps

### Step 1: Deploy Prerequisites

> **⚠️ Note**: You can re-use existing installations of the operators, ensure the correct versions are installed.

Deploy the prerequisite components first and wait for them to become ready:

```bash
oc apply -k ./docs/samples/llmisvc/ocp-4-18-setup/prerequisites/
```

Wait for the following components to become ready:

1. **cert-manager** - The cert-manager subscription in the `cert-manager-operator` namespace
2. **Red Hat OpenShift Service Mesh Operator** - The servicemeshoperator3 subscription in the `openshift-operators`
   namespace
3. **Leader Worker Set Operator** - The leader-worker-set subscription in the `openshift-lws-operator` namespace

Check readiness:

```bash
# Wait for cert-manager to be ready
oc wait --for=condition=CatalogSourcesUnhealthy=false subscription/openshift-cert-manager-operator -n cert-manager-operator --timeout=300s

# Wait for Service Mesh Operator to be ready
oc wait --for=condition=CatalogSourcesUnhealthy=false subscription/servicemeshoperator -n openshift-operators --timeout=300s

# Wait for Leader Worker Set Operator to be ready
oc wait --for=condition=CatalogSourcesUnhealthy=false subscription/leader-worker-set -n openshift-lws-operator --timeout=300s

# Verify Gateway API CRDs are installed
oc get crd gateways.gateway.networking.k8s.io
oc get crd gatewayclasses.gateway.networking.k8s.io
```

### Step 2: Apply Installation

After prerequisites are ready, deploy the main installation components:

```bash
oc apply -k ./docs/samples/llmisvc/ocp-4-18-setup/installation/
```

This will create:

1. **Leader Worker Set Operator Instance** - Creates the cluster-scoped LeaderWorkerSetOperator resource
2. **Istio CNI and Control Plane** - Sets up Istio v1.26.2 with gateway API inference extension support
3. **OpenShift AI Inference Gateway** - Creates the main gateway for inference traffic in the `openshift-ingress`
   namespace

Wait for installation components to be ready:

```bash
# Wait for Leader Worker Set Operator to be available
oc wait --for=condition=Available leaderworkersetoperator/cluster --timeout=300s

# Wait for Istio CNI to be ready
oc wait --for=condition=Ready istiocni/default --timeout=300s

# Wait for Istio control plane to be ready
oc wait --for=condition=Ready istio/default --timeout=300s

# Verify the gateway is created
oc get gateway openshift-ai-inference -n openshift-ingress
```

## Components Overview

### Prerequisites

- **Gateway API v1.2.0**: Provides the standard Gateway API CRDs
- **cert-manager v1.16.5**: Certificate management for Kubernetes (required by LWS operator)
- **Service Mesh Operator v3.1.0**: Red Hat's Istio-based service mesh operator
- **Leader Worker Set Operator v1.0.0**: Enables deploying pods as a unit of replication for AI/ML inference workloads

### Installation

- **Istio CNI**: Container Network Interface plugin for Istio
- **Istio Control Plane**: Service mesh control plane with gateway API inference extensions
- **Inference Gateway**: HTTP gateway for routing inference requests

## Next steps

- Deploy Red Hat OpenShift AI
- [Deploying a model by using the Distributed Inference Server with llm-d [Developer preview]](https://access.redhat.com/articles/7131048)