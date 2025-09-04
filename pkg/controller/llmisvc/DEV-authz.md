### Local Dev for authz

#### Pre-requisites

- [Setup OpenShift and kserve](./DEV.md#ocp-integration)

#### Apply Authentication for LLMISVC on OpenShift

**Environment variables**

```shell
export CTRL_NS=opendatahub            

export GATEWAY_NS=openshift-ingress
export AUTHORINO_NS=operators
export LLMISVC_NAME=facebook-opt-125m-single
export TEST_NS=llm-test

export NS=llm-test
export LLM_ISVC=docs/samples/llmisvc/opt-125m/llm-inference-service-facebook-opt-125m-cpu.yaml
export LLM_ISVC_NAME=$(cat $LLM_ISVC | yq .metadata.name)

```

##### Setup RedHat Connectivity Link

**Installation RHCL**

```shell
kubectl create ns kuadrant-system

kubectl apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: rhcl-operator
  namespace: kuadrant-system
spec:
  config:
    env:
      - name: ISTIO_GATEWAY_CONTROLLER_NAMES
        value: openshift.io/gateway-controller/v1
  channel: stable
  installPlanApproval: Automatic
  name: rhcl-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
---
kind: OperatorGroup
apiVersion: operators.coreos.com/v1
metadata:
  name: kuadrant
  namespace: kuadrant-system
spec:
  upgradeStrategy: Default
EOF

while [ $(kubectl get pod -n kuadrant-system  | wc -l) -le 1 ]; 
do
  echo "⏳ waiting for Kuadrant Pod to appear…"
  sleep 10
done
kubectl wait --for=condition=ready pod -l app=kuadrant -n kuadrant-system --timeout 150s

kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: kuadrant-system
EOF
```

**Create Pull Secret**

Refer to [this doc](https://docs.redhat.com/en/documentation/red_hat_connectivity_link/1.0/html-single/installing_connectivity_link_on_openshift/index#auth-registry-wasm-plugin)

You can get the secret from [this site](https://access.redhat.com/terms-based-registry/) and gateway namespace(`openshift-ingress`)

```shell
kubectl create secret docker-registry wasm-plugin-pull-secret -n openshift-ingress \ --docker-server=registry.redhat.io \ --docker-username=<your-registry-service-account-username >\ --docker-password=<your-registry-service-account-password>

```

**Update gateway label**
```shell
kubectl label gateway openshift-ai-inference -n openshift-ingress kuadrant.io/gateway="true"
```

**Authentication with KubernetesTokenReview**

```shell

# Set default audience
export AUDIENCE="https://kubernetes.default.svc"

# Check if OpenShift cluster has custom service account issuer
SA_ISSUER=$(oc get authentication cluster -o jsonpath='{.spec.serviceAccountIssuer}' -n openshift-authentication 2>/dev/null)

# Update AUDIENCE if custom issuer is found
if [[ -n "$SA_ISSUER" ]]; then
  # For ROSA cluster
  export AUDIENCE="$SA_ISSUER"
fi  

kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: llm-test-authn
  namespace: $TEST_NS
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: $LLM_ISVC_NAME-kserve-route
  defaults:
    strategy: merge
    rules:
      authentication:
        kubernetes-user:
          credentials:
            authorizationHeader: {}
          kubernetesTokenReview:
            audiences:
            - $AUDIENCE
          metrics: false
          priority: 0
      authorization:
        kubernetes-rbac:
          kubernetesSubjectAccessReview:
            resourceAttributes:
              group:
                value: serving.kserve.io/v1alpha1
              name:
                value: $LLM_ISVC_NAME
              namespace:
                value: $TEST_NS
              resource:
                value: LLMInferenceService
              subresource:
                value: ""
              verb:
                value: get
            user:
              selector: auth.identity.user.username
          metrics: false
          priority: 0
EOF

```

**Validation**

- Using external hostname (ROSA or metalLB)

```shell

# Expected - HTTP/1.1 401 Unauthorized
curl -v $(kubectl get llmisvc -n $TEST_NS -o=jsonpath='{.items[0].status.addresses[0].url}')/v1/completions  \
    -H "Content-Type: application/json" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }' 

# Expected - HTTP/1.1 200 OK
curl $(kubectl get llmisvc -n $TEST_NS -o=jsonpath='{.items[0].status.addresses[0].url}')/v1/completions  \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $(oc whoami -t)"  \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }'

```

- Using Gateway Route (this is for testing only)
```
MODEL_ID=facebook/opt-125m

oc expose svc/openshift-ai-inference-istio -n openshift-ingress --port http 
kubectl wait --for=condition=ready pod -l app.kubernetes.io/part-of=llminferenceservice -n $NS --timeout 150s
  
LB_HOST=$( kubectl get route/openshift-ai-inference-istio -n openshift-ingress -o=jsonpath='{.status.ingress[*].host}'  )


# Expected - HTTP/1.1 401 Unauthorized
curl -v http://$LB_HOST/$NS/$LLM_ISVC_NAME/v1/completions  \
    -H "Content-Type: application/json" \
    -d '{
        "model":"'"$MODEL_ID"'",
        "prompt": "San Francisco is a"
    }'    

# Expected - HTTP/1.1 200 OK
curl http://$LB_HOST/$NS/$LLM_ISVC_NAME/v1/completions  \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $(oc whoami -t)"  \
    -d '{
        "model":"'"$MODEL_ID"'",
        "prompt": "San Francisco is a"
    }'           
```
- Using internal hostname


```shell
TOKEN=$(oc whoami -t)

kubectl run --rm -i curl-test \
    --namespace "$TEST_NS" \
    --image=curlimages/curl --restart=Never -- \
    curl -v http://openshift-ai-inference-istio.$GATEWAY_NS.svc.cluster.local:80/$TEST_NS/$LLM_ISVC_NAME/v1/completions  -H "Content-Type: application/json"         -H "Authorization: Bearer $TOKEN"    -d '{ 
         "model": "facebook/opt-125m",
         "prompt": "San Francisco is a"
     }'
```
