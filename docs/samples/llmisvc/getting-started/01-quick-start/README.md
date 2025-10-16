
# Quick Start: Deploy Your First LLM Inference Service

This guide walks you through deploying your first LLM Inference Service using the Facebook OPT-125M model as an example.

## Prerequisites

Before starting, ensure you have:
- OpenShift cluster with KServe and LLM Inference Service components installed: [Setup OpenShift and KServe](../../ocp-setup-for-GA/README.md)
- `kubectl` or `oc` CLI configured to access your cluster
- A default GatewayClass and a default Gateway:
  ```bash
  oc create -f docs/samples/llmisvc/getting-started/01-quick-start/openshift-ai-inference-gateway.yaml
  ```


## Step 1: Create a Namespace

First, create a dedicated namespace for your LLM workload:

```bash
export TEST_NS=llm-test
kubectl create namespace ${TEST_NS}
```

## Step 2: Deploy the LLM Inference Service

Deploy the Facebook OPT-125M model using the provided example:

```bash
# Set the path to the example YAML
export LLM_ISVC=docs/samples/llmisvc/opt-125m-cpu/llm-inference-service-facebook-opt-125m-cpu.yaml
export LLM_ISVC_NAME=$(cat $LLM_ISVC | yq .metadata.name)

# Deploy the service
kubectl apply -n ${TEST_NS} -f ${LLM_ISVC}
oc annotate llmisvc/facebook-opt-125m-single security.opendatahub.io/enable-auth=false -n $TEST_NS
```

This creates an LLM Inference Service with the following configuration:
- **Model**: Facebook OPT-125M (downloaded from Hugging Face)
- **Runtime**: vLLM CPU backend
- **Resources**: 1 CPU core, 8Gi memory request
- **Replicas**: 1 instance

## Step 3: Verify Deployment

Check that your LLM Inference Service is running:

```bash
# Check the llmisvc status
kubectl get llminferenceservice -n ${TEST_NS}

# Watch for the pods to become ready
kubectl get pods -n ${TEST_NS} -w
```

Wait until the pod shows `Running` status and all containers are ready.

## Step 4: Test the Service

Once deployed, test your LLM service using the OpenAI-compatible API:

### Get the Service Endpoint

```bash
# Get the load balancer URL (for cloud environments)
export LB_URL=$(kubectl get llmisvc facebook-opt-125m-single -n ${TEST_NS} -o=jsonpath='{.status.url}')

```

### Send a Completion Request

```bash
curl "${LB_URL}/v1/completions"  \
    -H "Content-Type: application/json" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a",
        "max_tokens": 16,
        "temperature": 0.7
    }'
```

### Expected Response

You should receive a response similar to:

```json
{
  "id": "cmpl-f0601f1b-66cc-4f0c-bd0c-cc93c8afd9ec",
  "object": "text_completion",
  "created": 1751477229,
  "model": "facebook/opt-125m",
  "choices": [
    {
      "index": 0,
      "text": " big place and I'd imagine it will stay that way. Until the US rel",
      "logprobs": null,
      "finish_reason": "length",
      "stop_reason": null,
      "prompt_logprobs": null
    }
  ],
  "usage": {
    "prompt_tokens": 5,
    "total_tokens": 21,
    "completion_tokens": 16,
    "prompt_tokens_details": null
  },
  "kv_transfer_params": null
}
```

## Step 5: Clean Up

When you're done testing, clean up the resources:

```bash
# Delete the LLM Inference Service
kubectl delete llminferenceservice -n ${TEST_NS} --all

# Delete the namespace
kubectl delete namespace ${TEST_NS}
```
