# LLMInferenceService Authentication

The LLMInferenceService authentication relies on Authorino, which is provided by Red Hat Connectivity Link (RHCL). Therefore, RHCL 1.1.1 or later is a required operator. Communication between the Gateway API ingress pod and the Authorino pod is encrypted via SSL. For detailed configuration, please refer to [the related documentation.](../../ocp-setup-for-GA/README.md)

Starting from RHOAI 3.0, authentication is enabled by default. This means that when you create an LLMInferenceService, you must include a valid token in your request.

In this tutorial, we will explore:

- How to enable or disable authentication
- What roles are required for the ServiceAccount
- How to perform inference requests with authentication enabled

## Prerequisites

Before starting, ensure you have completed:
- [Setup OpenShift and KServe](../../ocp-setup-for-GA/README.md)
- [Quick Start tutorial](../01-quick-start/README.md)
- Deployed the LLMInferenceService using Facebook OPT-125M model

## How to Enable or Disable Authentication

### Enable Authentication

If Authorino is not installed before the odh-model-controller starts, authentication will be opted out by default. You can restart the odh-model-controller to enable it once Authorino is available:

```bash
oc delete pod -n opendatahub -l app=odh-model-controller
```

**Verify that Global AuthPolicy is Created:**

```bash
oc get authPolicy -n openshift-ingress
```

Expected output:
```
NAME                           AGE
openshift-ai-inference-authn   10m
```

✅ Your LLMInferenceService is now protected.

### Disable Authentication (Optional)

To allow anonymous access to a specific LLMInferenceService:

```bash
oc annotate llmisvc/facebook-opt-125m-single security.opendatahub.io/enable-auth=false -n $TEST_NS
```

**Verify Anonymous AuthPolicy is Created:**

```bash
oc get authpolicy -n $TEST_NS
```

Expected output:
```
NAME                                          AGE
facebook-opt-125m-single-kserve-route-authn   5s
```

✅ Your LLMInferenceService now allows anonymous access.

## What Roles are Required for the ServiceAccount

To access the inference endpoint, the ServiceAccount must have permission to get the corresponding LLMInferenceService.

**Permission Scope:**
- **With resourceNames**: Access restricted to specific LLMInferenceService(s)
- **Without resourceNames**: Access to all LLMInferenceServices in the namespace

### Create ServiceAccount with Proper RBAC

```bash
# Set ServiceAccount name
SA_NAME=right-user

# Create ServiceAccount
oc create sa $SA_NAME -n $TEST_NS

# Create Role and RoleBinding
cat <<EOF | oc apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: llm-inferenceservice-reader
  namespace: $TEST_NS
rules:
  - apiGroups: ["serving.kserve.io"]
    resources: ["llminferenceservices"]
    verbs: ["get"]
    # resourceNames: ["facebook-opt-125m-single"] # Uncomment to restrict to specific service
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: llm-inferenceservice-reader-binding
  namespace: $TEST_NS
subjects:
  - kind: ServiceAccount
    name: $SA_NAME
    namespace: $TEST_NS
roleRef:
  kind: Role
  name: llm-inferenceservice-reader
  apiGroup: rbac.authorization.k8s.io
EOF
```

### Verify Permissions and Generate Token

```bash
# Check if ServiceAccount has proper permissions
oc auth can-i get llminferenceservices -n $TEST_NS --as=system:serviceaccount:$TEST_NS:$SA_NAME

# Generate JWT Token
export TEST_TOKEN=$(oc create token $SA_NAME -n $TEST_NS)
```

Expected output for can-i command: `yes`

## How to Perform Inference Requests with Authentication Enabled

### Using External Hostname (ROSA or MetalLB)

**Test Without Token (Expected to Fail):**

```bash
# Expected - HTTP/1.1 401 Unauthorized
curl -v $(kubectl get llmisvc -n $TEST_NS -o=jsonpath='{.items[0].status.addresses[0].url}')/v1/completions \
    -H "Content-Type: application/json" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }'
```

**Test With Valid Token (Expected to Succeed):**

```bash
# Expected - HTTP/1.1 200 OK
curl -v $(kubectl get llmisvc -n $TEST_NS -o=jsonpath='{.items[0].status.addresses[0].url}')/v1/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${TEST_TOKEN}" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }'
```

### Using Internal Hostname

```bash
kubectl run --rm -i curl-test \
    --namespace "$TEST_NS" \
    --image=quay.io/jooholee/curl \
    --restart=Never -- \
    curl -v http://openshift-ai-inference-openshift-default.openshift-ingress.svc.cluster.local:80/llm-test/facebook-opt-125m-single/v1/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $TEST_TOKEN" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }'
```

## Cleanup (Optional)

To remove the resources created in this tutorial:

```bash
# Delete ServiceAccount
oc delete sa $SA_NAME -n $TEST_NS

# Delete Role and RoleBinding
oc delete role llm-inferenceservice-reader -n $TEST_NS
oc delete rolebinding llm-inferenceservice-reader-binding -n $TEST_NS
```
