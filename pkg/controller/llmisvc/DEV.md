### Local Dev

#### Deploying LLMInferenceService controller locally

> [!IMPORTANT]
> If you are using quay.io make sure to change kserve binary img repos visibility to public!

##### Using `kind`

```shell
kind create cluster -n "kserve-llm-d"

go install sigs.k8s.io/cloud-provider-kind@latest

cloud-provider-kind > /dev/null 2>&1 &
```

##### Using `minikube`

```shell
minikube start --cpus='12' --memory='16G' --kubernetes-version=v1.33.1
minikube addons enable metallb

IP=$(minikube ip)
PREFIX=${IP%.*}
START=${PREFIX}.200
END=${PREFIX}.235

kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  namespace: metallb-system
  name: config
data:
  config: |
    address-pools:
    - name: default
      protocol: layer2
      addresses:
      - ${START}-${END}
EOF
```

#### Install KServe (dev) in the created cluster

```shell
make deploy-dev-llm -e KO_DOCKER_REPO=<YOUR_REPO>
```

#### Validation

##### pytest

Set up pytest
```shell
cd python/kserve 
python -m venv .venv
pip install -e .
pip install pytest pytest-asyncio requests portforward Jinja2 pytest-xdist
cd -
```

Run the test

```shell
# Use pytest markers for filtering

# Run only CPU tests
./test/scripts/gh-actions/run-e2e-tests.sh "llminferenceservice and cluster_cpu" 1 "istio-gatewayapi-ext"

# Run only NVIDIA GPU tests
./test/scripts/gh-actions/run-e2e-tests.sh "llminferenceservice and cluster_nvidia" 1 "istio-gatewayapi-ext"

# Run all GPU tests (any vendor: amd, nvidia, intel)
./test/scripts/gh-actions/run-e2e-tests.sh "llminferenceservice and (cluster_amd or cluster_nvidia or cluster_intel)" 1 "istio-gatewayapi-ext"

# Run CPU and AMD GPU tests only
./test/scripts/gh-actions/run-e2e-tests.sh "llminferenceservice and (cluster_cpu or cluster_amd)" 1 "istio-gatewayapi-ext"

# Run all LLM inference service tests
./test/scripts/gh-actions/run-e2e-tests.sh "llminferenceservice" 1 "istio-gatewayapi-ext"

Starting E2E functional tests ...
No parallelism requested for pytest. Will use default value of 1
pytest -m 'llminferenceservice and cluster_cpu' --ignore=qpext --log-cli-level=INFO -n 1 --dist worksteal --network-layer istio-gatewayapi-ext
===================================================================================== test session starts =====================================================================================
platform linux -- Python 3.12.11, pytest-8.4.1, pluggy-1.6.0
rootdir: /home/bartek/code/redhat/model-serving/kserve/kserve-test/test/e2e
configfile: pytest.ini
plugins: anyio-4.9.0, xdist-3.8.0, asyncio-1.1.0
asyncio: mode=Mode.STRICT, asyncio_default_fixture_loop_scope=None, asyncio_default_test_loop_scope=function
1 worker [1 item]s / 1 error
scheduling tests via WorkStealingScheduling

llmisvc/test_llm_inference_service.py::test_llm_inference_service[managed-single-cpu-fb-opt-125m]
[gw0] [100%] PASSED llmisvc/test_llm_inference_service.py::test_llm_inference_service[managed-single-cpu-fb-opt-125m]
```

##### Manual

Create LLMInferenceService, e.g.:

```shell
NS=llm-test
kubectl create namespace ${NS} || true

LLM_ISVC=docs/samples/llmisvc/opt-125m/llm-inference-service-facebook-opt-125m-cpu.yaml
LLM_ISVC_NAME=$(cat $LLM_ISVC | yq .metadata.name)

kubectl apply -n ${NS} -f ${LLM_ISVC}
```

```shell
LB_IP=$(kubectl get svc/kserve-ingress-gateway-istio -n kserve -o=jsonpath='{.status.loadBalancer.ingress[0].ip}')

curl http://${LB_IP}/${NS}/${LLM_ISVC_NAME}/v1/completions  \
    -H "Content-Type: application/json" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }'
```

You should see:

```shell
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

Or by using populated URLs from LLMInferenceService status:

```shell
curl $(kubectl get llmisvc -n $NS -o=jsonpath='{.items[0].status.addresses[0].url}')/v1/completions  \
    -H "Content-Type: application/json" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }' | jq
```

> [!NOTE]
> Actual address in KinD setup is considered local, hence the jsonpath above.

you should see a similar output:

```shell
{
  "id": "cmpl-8482188a-d941-4d8a-96f8-c001e0e03624",
  "object": "text_completion",
  "created": 1751543644,
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

#### Trigger disaggregated serving (P/D)

Generally, this doesn't _always_ reliably trigger separate prefill, since the scheduler is looking at the prompt prefix,
but it should work the "first" time.

```shell
curl -v -k -XPOST -H "Content-Type: application/json" \
  -d '{
  "model": "facebook/opt-125m",
  "prompt": "Delve into the multifaceted implications of a fully disaggregated cloud architecture, specifically where the compute plane (P) and the data plane (D) are independently deployed and managed for a geographically distributed, high-throughput, low-latency microservices ecosystem. Beyond the fundamental challenges of network latency and data consistency, elaborate on the advanced considerations and trade-offs inherent in such a setup: 1. Network Architecture and Protocols: How would the network fabric and underlying protocols (e.g., RDMA, custom transport layers) need to evolve to support optimal performance and minimize inter-plane communication overhead, especially for synchronous operations? Discuss the role of network programmability (e.g., SDN, P4) in dynamically optimizing routing and traffic flow between P and D. 2. Advanced Data Consistency and Durability: Explore sophisticated data consistency models (e.g., causal consistency, strong eventual consistency) and their applicability in balancing performance and data integrity across a globally distributed data plane. Detail strategies for ensuring data durability and fault tolerance, including multi-region replication, intelligent partitioning, and recovery mechanisms in the event of partial or full plane failures. 3. Dynamic Resource Orchestration and Cost Optimization: Analyze how an orchestration layer would intelligently manage the independent scaling of compute (P) and data (D) resources, considering fluctuating workloads, cost efficiency, and performance targets (e.g., using predictive analytics for resource provisioning). Discuss mechanisms for dynamically reallocating compute nodes to different data partitions based on workload patterns and data locality, potentially involving live migration strategies. 4. Security and Compliance in a Distributed Landscape: Address the enhanced security perimeter challenges, including securing communication channels between P and D (encryption in transit, mutual TLS), fine-grained access control to data at rest and in motion, and identity management across disaggregated components. Discuss how such an architecture impacts compliance with regulatory frameworks (e.g., GDPR, HIPAA) concerning data sovereignty, privacy, and auditability. 5. Operational Complexity and Observability: Examine the increased complexity in monitoring, logging, and tracing across highly decoupled compute and data planes. What specialized tooling and practices (e.g., distributed tracing with OpenTelemetry, advanced AIOps) would be essential? How would incident response and troubleshooting differ in this disaggregated environment compared to traditional integrated systems? Consider the challenges of pinpointing root causes across independent failures. 6. Real-world Applicability and Future Trends: Identify specific industries or use cases (e.g., high-frequency trading, IoT edge processing, large language model inference) where the benefits of P/D disaggregation would strongly outweigh its complexities. Conclude by speculating on emerging technologies or paradigms (e.g., serverless compute functions directly interacting with object storage, in-memory disaggregation) that could further drive or transform P/D disaggregation in cloud computing.", 
  "stream": false, 
  "max_tokens": 50}' http://${LB_IP}/${NS}/${LLM_ISVC_NAME}/v1/completions | jq
```

#### Persistent Volume (PV) for model storage

Create a PV and a PVC to store the model:

```shell
kubectl apply -n ${NS} -f - <<EOF
apiVersion: v1
kind: PersistentVolume
metadata:
  name: opt-125m-pv
spec:
  accessModes:
  - ReadWriteOnce
  capacity:
    storage: 3Gi
  hostPath:
    path: /data/models/opt-125m
    type: DirectoryOrCreate
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: opt-125m-pvc
spec:
  storageClassName: ""
  volumeName: opt-125m-pv
  resources:
    requests:
      storage: 800Mi
  accessModes:
  - ReadWriteOnce
EOF
```

To download a model to the PV, the KServe storage initializer can be used by
creating a Job:

```shell
kubectl apply -n ${NS} -f - <<EOF
STORAGE_INIT_IMG=$(kubectl get cm -n kserve -o jsonpath="{.data.storageInitializer}" inferenceservice-config | jq -r '.image')

kubectl apply -n ${NS} -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: opt-125m-download-job
spec:
  parallelism: 1
  completions: 1
  template:
    spec:
      restartPolicy: Never
      securityContext:
        runAsUser: 0
      volumes:
      - name: pvc
        persistentVolumeClaim:
          claimName: opt-125m-pvc
          readOnly: false
      containers:
      - image: ${STORAGE_INIT_IMG}
        name: storage-initializer
        args:
        - hf://facebook/opt-125m
        - /tmp/model
        resources:
          requests: 
            cpu: "100m"
            memory: "100Mi"
          limits:
            cpu: "1"
            memory: "1Gi"
        volumeMounts:
        - name: pvc
          mountPath: "/tmp/model"
          readOnly: false
EOF
```

Deploy the model from the persistent volume:

```shell
yq '.spec.model.uri="pvc://opt-125m-pvc"' ${LLM_ISVC} | kubectl apply -n ${NS} -f -
```


---
#### OCP integration

*OpenShift Cluster 4.19+*

##### Using `openshift ROSA cluster`

You just need to login to ROSA cluster

```
kubectl login $OCP_API_SERVER
```

##### Using `openshift local`

```shell
crc setup
crc config set memory 25600
crc config set cpus 10
crc config set disk-size 150
crc config set kubeadmin-password kubeadmin
crc config set enable-cluster-monitoring false

# Download secret from https://developers.redhat.com/products/openshift-local/overview
crc start -p ~/pull-secret.txt

oc login -u kubeadmin https://api.crc.testing:6443
```
*Pre-requisites*
- Install Cert-Manager
- Install LWS operator (This should be installed by user)

**Install Cert-Manager**

```shell
kubectl create namespace cert-manager-operator || true

cat<<EOF | kubectl create -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  annotations:
      olm.providedAPIs: CertManager.v1alpha1.operator.openshift.io,Certificate.v1.cert-manager.io,CertificateRequest.v1.cert-manager.io,Challenge.v1.acme.cert-manager.io,ClusterIssuer.v1.cert-manager.io,Issuer.v1.cert-manager.io,IstioCSR.v1alpha1.operator.openshift.io,Order.v1.acme.cert-manager.io
  name: cert-manager-operator
  namespace: cert-manager-operator
spec:
  upgradeStrategy: Default
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: openshift-cert-manager-operator
  namespace: cert-manager-operator
spec:
  channel: stable-v1
  installPlanApproval: Automatic
  name: openshift-cert-manager-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF

while [ $(kubectl get pod -n cert-manager-operator  | wc -l) -le 1 ]; 
do
  echo "⏳ waiting for Cert-Manager Pod to appear…"
  sleep 10
done
kubectl wait pod -l name=cert-manager-operator -n cert-manager-operator --for=condition=Ready --timeout=120s 
```

**Install LWS Operator**

Install the official Leader Worker Set operator from the Red Hat Operators catalog:

```shell
kubectl create ns openshift-lws-operator || true

cat <<EOF | kubectl create -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: leader-worker-set
  namespace: openshift-lws-operator
spec:
  targetNamespaces:
  - openshift-lws-operator
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: leader-worker-set
  namespace: openshift-lws-operator
spec:
  channel: stable-v1.0
  installPlanApproval: Automatic
  name: leader-worker-set
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF

until kubectl get crd leaderworkersetoperators.operator.openshift.io &> /dev/null; do
  echo "⏳ waiting for CRD to appear…"
  sleep 2
done

# Wait until the pod is created
oc wait pod -l name=openshift-lws-operator -n openshift-lws-operator --for=condition=Ready --timeout=120s

kubectl wait \
  --for=condition=Established \
  --timeout=60s \
  crd/leaderworkersetoperators.operator.openshift.io

cat <<EOF | kubectl create -f -
apiVersion: operator.openshift.io/v1
kind: LeaderWorkerSetOperator
metadata:
  name: cluster
  namespace: openshift-lws-operator
spec:
  managementState: Managed
  logLevel: Normal
  operatorLogLevel: Normal
EOF
```

**Deploy Kserve** 
*- option 1 - through opendatahub-operator catalogsource*

Create OpenDataHub subscription
```shell
cat <<EOF| kubectl create -f -
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

Create DSCI and DSC 
```shell
cat <<EOF| kubectl create -f -
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
kubectl wait --for=condition=ready pod -l control-plane=kserve-controller-manager -n opendatahub  --timeout=300s
```

*- option 2 - Using overlay/odh*

A new CRD related objects will be added 
  - LLMIsvc/LLMIsvcConfig CRD
  - GIE CRD
  - Webhook
  - `well-know preset` LlmIsvcConfig in the controller namespace

```shell
kubectl create ns opendatahub || true

kubectl kustomize config/crd/ | kubectl apply --server-side=true -f -
until kubectl get crd llminferenceserviceconfigs.serving.kserve.io &> /dev/null; do
  echo "⏳ waiting for CRD to appear…"
  sleep 2
done
kubectl wait --for=condition=Established --timeout=60s crd/llminferenceserviceconfigs.serving.kserve.io

# Use Kustomize 5.7+
kustomize build config/overlays/odh | kubectl apply  --server-side=true --force-conflicts -f -

kubectl wait --for=condition=ready pod -l control-plane=kserve-controller-manager -n opendatahub  --timeout=300s
```

**Create a default GatewayClass**

- OpenShift 4.19.9+
```shell
cat<<EOF|oc create -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: openshift-default
spec:
  controllerName: "openshift.io/gateway-controller/v1"
EOF
```

**Create a gateway**
```shell
Ikubectl create namespace ${INGRESS_NS} || true

kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: openshift-ai-inference
  namespace: openshift-ingress
spec:
  gatewayClassName: openshift-default
  listeners:
   - name: http
     port: 80
     protocol: HTTP
     allowedRoutes:
       namespaces:
         from: All
  infrastructure:
    labels:
      serving.kserve.io/gateway: kserve-ingress-gateway
EOF

# If LoadBalancer IP is not available, annotate the Gateway to use a NodePort Service
# kubectl annotate gateways.gateway.networking.k8s.io -n openshift-ingress openshift-ai-inference networking.istio.io/service-type=NodePort --overwrite

kubectl wait gateways.gateway.networking.k8s.io -n openshift-ingress openshift-ai-inference --timeout=5m --for=condition=programmed
```

You can verify if istiod pod is running in openshift-ingress namespace.
```shell
oc get pod -n openshift-ingress -l operator.istio.io/component=Pilot
```

### Deploy CPU model

```shell
NS=llm-test
oc new-project "${NS}" || true

LLM_ISVC=docs/samples/llmisvc/opt-125m-cpu/llm-inference-service-facebook-opt-125m-cpu.yaml
LLM_ISVC_NAME=$(cat $LLM_ISVC | yq .metadata.name)

kubectl get ns $NS||kubectl create ns $NS
kubectl apply -n ${NS} -f ${LLM_ISVC}

oc wait llminferenceservice --for=condition=ready --all --timeout=300s
```

### Deploy DP + EP model with P/D on GPUS

```shell
NS=llm-test
oc new-project "${NS}" || true

LLM_ISVC=./docs/samples/llmisvc/dp-ep/llm-inference-service-dp-ep-pd-deepseek-gpu.yaml
LLM_ISVC_NAME=$(cat $LLM_ISVC | yq .metadata.name)

kubectl get ns $NS || kubectl create ns $NS
kubectl apply -n ${NS} -f ${LLM_ISVC}

oc wait llminferenceservice --for=condition=ready --all --timeout=600s
```

#### Example requests

```shell
curl -v "${LB_URL}/v1/chat/completions"   -H "Content-Type: application/json"   -d '{
    "model": "deepseek-ai/DeepSeek-V2-Lite-Chat",
    "messages": [
      {
        "role": "system",
        "content": "You are a helpful and knowledgeable AI assistant specializing in world history and geography. Your name is 'Atlas'. When responding, you must be objective, factual, and provide detailed context. For any locations mentioned, you should include their region and country. For historical events, always provide the relevant time period or specific dates. You should structure your answers clearly and concisely. When asked about a city, begin with a brief summary of its significance before providing historical details in chronological order. Always use LaTeX formatting for any numerical data or coordinates, like so: $\\text{41.1171° N, 16.8719° E}$."
      },
      {
        "role": "user",
        "content": "Tell me about the city of Bari."
      }
    ],
    "max_tokens": 1500,
    "temperature": 0.5
  }' | jq
```

```shell
curl -v "${LB_URL}/v1/chat/completions"   -H "Content-Type: application/json"   -d '{
    "model": "deepseek-ai/DeepSeek-V2-Lite-Chat",
    "messages": [
      {
        "role": "system",
        "content": "You are a helpful and knowledgeable AI assistant specializing in world history and geography. Your name is 'Atlas'. When responding, you must be objective, factual, and provide detailed context. For any locations mentioned, you should include their region and country. For historical events, always provide the relevant time period or specific dates. You should structure your answers clearly and concisely. When asked about a city, begin with a brief summary of its significance before providing historical details in chronological order. Always use LaTeX formatting for any numerical data or coordinates, like so: $\\text{41.1171° N, 16.8719° E}$."
      },
      {
        "role": "user",
        "content": "Delve into the multifaceted implications of a fully disaggregated cloud architecture, specifically where the compute plane (P) and the data plane (D) are independently deployed and managed for a geographically distributed, high-throughput, low-latency microservices ecosystem. Beyond the fundamental challenges of network latency and data consistency, elaborate on the advanced considerations and trade-offs inherent in such a setup: 1. Network Architecture and Protocols: How would the network fabric and underlying protocols (e.g., RDMA, custom transport layers) need to evolve to support optimal performance and minimize inter-plane communication overhead, especially for synchronous operations? Discuss the role of network programmability (e.g., SDN, P4) in dynamically optimizing routing and traffic flow between P and D. 2. Advanced Data Consistency and Durability: Explore sophisticated data consistency models (e.g., causal consistency, strong eventual consistency) and their applicability in balancing performance and data integrity across a globally distributed data plane. Detail strategies for ensuring data durability and fault tolerance, including multi-region replication, intelligent partitioning, and recovery mechanisms in the event of partial or full plane failures. 3. Dynamic Resource Orchestration and Cost Optimization: Analyze how an orchestration layer would intelligently manage the independent scaling of compute (P) and data (D) resources, considering fluctuating workloads, cost efficiency, and performance targets (e.g., using predictive analytics for resource provisioning). Discuss mechanisms for dynamically reallocating compute nodes to different data partitions based on workload patterns and data locality, potentially involving live migration strategies. 4. Security and Compliance in a Distributed Landscape: Address the enhanced security perimeter challenges, including securing communication channels between P and D (encryption in transit, mutual TLS), fine-grained access control to data at rest and in motion, and identity management across disaggregated components. Discuss how such an architecture impacts compliance with regulatory frameworks (e.g., GDPR, HIPAA) concerning data sovereignty, privacy, and auditability. 5. Operational Complexity and Observability: Examine the increased complexity in monitoring, logging, and tracing across highly decoupled compute and data planes. What specialized tooling and practices (e.g., distributed tracing with OpenTelemetry, advanced AIOps) would be essential? How would incident response and troubleshooting differ in this disaggregated environment compared to traditional integrated systems? Consider the challenges of pinpointing root causes across independent failures. 6. Real-world Applicability and Future Trends: Identify specific industries or use cases (e.g., high-frequency trading, IoT edge processing, large language model inference) where the benefits of P/D disaggregation would strongly outweigh its complexities. Conclude by speculating on emerging technologies or paradigms (e.g., serverless compute functions directly interacting with object storage, in-memory disaggregation) that could further drive or transform P/D disaggregation in cloud computing."
      }
    ],
    "max_tokens": 1500,
    "temperature": 0.5
  }' | jq
```

```shell
curl -v "${LB_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-ai/DeepSeek-V2-Lite-Chat",
    "messages": [
      {
        "role": "system",
        "content": "You are a highly specialized AI analyst named ''Logos-9'', designed to function as a neutral arbiter and synthesizer of complex, multi-domain information. Your core directive is to deconstruct multifaceted propositions into their constituent parts, evaluate them with strict objectivity, and present a balanced, evidence-based analysis. You must adhere to the following operational protocols without deviation:\n\n1.  **Neutrality is Paramount**: You must never adopt a stance or express a preference for any side of an argument. Your language must remain dispassionate, formal, and rigorously academic. Avoid all emotive, persuasive, or speculative language that is not explicitly framed as a component of a documented viewpoint.\n\n2.  **Structured Analysis**: For every query, you must first briefly paraphrase the user''s core proposition to confirm understanding. Then, explicitly state the analytical framework you will use (e.g., pro-con analysis, SWOT analysis, causal chain analysis). Structure your response with clear headings, subheadings, and bullet points to ensure maximum readability.\n\n3.  **Argument Deconstruction**: Identify and isolate the key arguments, assumptions, and variables associated with the proposition. When analyzing economic or technical factors, you are required to use precise terminology and formal notation. All mathematical, statistical, or scientific formulas and variables must be rendered in LaTeX. For example, represent probability as $P(x)$, Levelized Cost of Energy as $\\text{LCOE}$, and capital expenditures as $C_{cap}$.\n\n4.  **Evidence and Fallacy Identification**: Your analysis must be grounded in established data. While you do not have live internet access, you must reference the types of data required to validate claims (e.g., peer-reviewed studies, government reports, market data). You must also identify and label potential logical fallacies (e.g., ''ad hominem'', ''straw man'', ''appeal to authority'') that could be used by proponents or opponents of a given argument.\n\n5.  **Acknowledge Limitations**: Your knowledge base has a cutoff of early 2025. You must explicitly state this if a query involves information or events beyond that date. If a claim is highly speculative or currently unverifiable due to a lack of data, you must clearly label it as such.\n\nYour ultimate goal is not to provide an ''answer'', but to provide a structured intellectual toolkit that empowers the user to better understand the complexities of the topic at hand. Your function is that of a detached analytical engine, not a conversational partner."
      },
      {
        "role": "user",
        "content": "Please act as Logos-9 and analyze the following complex proposition: ''By the year 2080, nuclear fusion, powered by Deuterium-Tritium ($D-T$) or alternative fuel cycles, will surpass nuclear fission in terms of global installed capacity (GWe) and economic viability.'' Your analysis must perform the following tasks:\n\n1.  Deconstruct the primary technological and engineering hurdles for both D-T and advanced-fuel fusion reactors that must be overcome for this proposition to be realized.\n2.  Create a comparative economic analysis, identifying the key variables that influence the Levelized Cost of Energy ($\\text{LCOE}$) for fusion versus modern Gen-IV fission reactors. Include factors like capital costs ($C_{cap}$), fuel costs ($C_{fuel}$), and operational/decommissioning costs ($C_{ops}$).\n3.  Analyze the critical geopolitical and regulatory shifts that would need to occur to facilitate such a massive transition in the global energy infrastructure.\n4.  Identify one major logical fallacy that proponents of fusion might currently be susceptible to and one that proponents of fission might use in arguing against this transition.\n5.  Conclude with a neutral synthesis of the most critical dependencies and uncertainties that will determine the outcome."
      }
    ],
    "max_tokens": 3000,
    "temperature": 0.2,
    "stream": false
  }' | jq
```

```shell
curl -v -X POST "${LB_URL}/v1/chat/completions" \
-H "Content-Type: application/json" \
-d '{
    "model": "deepseek-ai/DeepSeek-V2-Lite-Chat",
    "messages": [
        {
            "role": "system",
            "content": "You are FinBot Pro, a highly sophisticated AI financial services assistant. Your purpose is to provide detailed, accurate, and educational information about personal finance, investment strategies, economic principles, and financial products. \n\n**Core Directives:**\n1.  **Persona**: You are an expert analyst and educator. Your tone should be professional, objective, and clear. Avoid overly casual language or unsubstantiated claims.\n2.  **Capabilities**: You can explain complex topics like portfolio theory, derivative instruments, tax-advantaged accounts (e.g., 401(k), IRA, HSA), asset allocation, risk management, and macroeconomic indicators. You can perform calculations based on user-provided data, such as compound interest projections, but you must state your assumptions clearly (e.g., assumed rate of return).\n3.  **Strict Constraints**: \n    - **No Financial Advice**: You MUST NOT provide personalized financial advice, recommend specific stocks, funds, or securities to buy or sell. Always preface sensitive responses with a clear disclaimer: \"This is for informational purposes only and does not constitute financial advice. Consult with a qualified financial professional before making any investment decisions.\"\n    - **Data Privacy**: Do not ask for or store personally identifiable information (PII) like names, account numbers, or contact details.\n    - **Knowledge Cutoff**: Your knowledge of market data and regulations is current up to Q2 2025. You must state this if a user asks about very recent events.\n4.  **Formatting**: Use Markdown for structuring your responses (headings, lists, bolding) to improve readability. Use LaTeX for all mathematical formulas and notations, such as the formula for future value: $FV = PV (1 + r)^n$."
        },
        {
            "role": "user",
            "content": "I need a comprehensive analysis of my retirement planning situation. I am 35 years old and plan to retire at 65. My current retirement savings are $250,000, all in a traditional 401(k). I contribute $22,500 annually. My risk tolerance is moderately aggressive. \n\nPlease address the following points:\n1.  Project the future value of my 401(k) at retirement. Use a reasonable annual growth rate for a moderately aggressive portfolio and show the formula you used.\n2.  Suggest a sample asset allocation for my portfolio (e.g., percentage in domestic stocks, international stocks, bonds, etc.) that aligns with my age and risk tolerance. Explain the rationale behind this allocation.\n3.  I am considering opening a Roth IRA in addition to my 401(k). Explain the key differences in tax treatment between my traditional 401(k) and a Roth IRA, especially concerning contributions and withdrawals in retirement.\n4.  My employer'\''s 401(k) plan offers a target-date fund, a large-cap US equity index fund, and an aggregate bond index fund. How could I use these three options to implement the asset allocation you suggested? What is portfolio rebalancing and why would it be important in this context?"
        }
    ],
    "max_tokens": 2048,
    "temperature": 0.4,
    "top_p": 0.9
}' | jq
```

```shell
curl -v -X POST "${LB_URL}/v1/chat/completions" \
-H "Content-Type: application/json" \
-d '{
    "model": "Qwen/Qwen3-Coder-30B-A3B-Instruct",
    "messages": [
        {
            "role": "system",
            "content": "You are DevBot Pro, a highly sophisticated AI developer assistant. Your purpose is to provide detailed, accurate, and educational information about software design and development."
        },
        {
            "role": "user",
            "content": "How do I implement AllReduce with Nvidia NCCL?"
        }
    ],
    "max_tokens": 2048,
    "temperature": 0.4,
    "top_p": 0.9
}' | jq
```

```shell
curl -v -X POST "${LB_URL}/v1/chat/completions" \
-H "Content-Type: application/json" \
-d '{
    "model": "deepseek-ai/DeepSeek-R1-0528",
    "messages": [
        {
            "role": "system",
            "content": "You are DevBot Pro, a highly sophisticated AI developer assistant. Your purpose is to provide detailed, accurate, and educational information about software design and development."
        },
        {
            "role": "user",
            "content": "How do I implement AllReduce with Nvidia NCCL?"
        }
    ],
    "max_tokens": 2048,
    "temperature": 0.4,
    "top_p": 0.9
}' | jq
```

#### Example evaluation

##### Using lm_eval directory

```shell
MODEL="deepseek-ai/DeepSeek-V2-Lite-Chat"
LB_URL=$(kubectl get llmisvc deepseek-v2-lite-chat -o=jsonpath='{.status.addresses[0].url}')

echo "LB_URL = $LB_URL - MODEL = ${MODEL}"

uv venv
source .venv/bin/activate
uv pip install "lm_eval[api]"
lm_eval --model local-completions --tasks gsm8k \
    --model_args model=${MODEL},base_url=${LB_URL}/v1/completions,num_concurrent=50,max_retries=3,tokenized_requests=False
```

##### Using lm-eval job

```shell
kubectl apply -k test/llmisvc/eval/overlays/deepseek-live-chat/

echo "Waiting 5 seconds for the pod to be created ..."
sleep 5

kubectl logs -l=batch.kubernetes.io/job-name=lm-eval-deepseek-v2-lite-chat -f
```

Example Output:
```shell
# ...

2025-08-13:06:40:11 INFO     [loggers.evaluation_tracker:280] Output path not provided, skipping saving results aggregated
local-completions (model=deepseek-ai/DeepSeek-V2-Lite-Chat,base_url=http://openshift-ai-inference-istio.openshift-ingress.svc.cluster.local/llm-test/deepseek-v2-lite-chat/v1/completions,num_concurrent=100,max_retries=3,tokenized_requests=False), gen_kwargs: (None), limit: None, num_fewshot: None, batch_size: 1
|     Tasks      |Version|     Filter     |n-shot|  Metric   |   | Value |   |Stderr|
|----------------|------:|----------------|-----:|-----------|---|------:|---|-----:|
|gsm8k           |      3|flexible-extract|     5|exact_match|↑  | 0.6550|±  |0.0131|
|                |       |strict-match    |     5|exact_match|↑  | 0.6490|±  |0.0131|
|hellaswag       |      1|none            |     0|acc        |↑  | 0.6213|±  |0.0048|
|                |       |none            |     0|acc_norm   |↑  | 0.8051|±  |0.0040|
|lambada_openai  |      1|none            |     0|acc        |↑  | 0.4493|±  |0.0069|
|                |       |none            |     0|perplexity |↓  |12.4125|±  |0.4293|
|abstract_algebra|      1|none            |     0|acc        |↑  | 0.3200|±  |0.0469|

--- lm_eval run finished successfully ---
+ echo --- lm_eval run finished successfully ---
```

#### Validation

**OpenShift Cluster**
```shell
LB_URL=$(kubectl get llmisvc facebook-opt-125m-single  -o=jsonpath='{.status.url}')

curl "${LB_URL}/v1/completions"  \
    -H "Content-Type: application/json" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }'
```

**OpenShift Local**

*Using Gateway Route (this is for testing only)*
```shell
MODEL_ID=facebook/opt-125m

oc expose svc/openshift-ai-inference-$GW_CLASS_NAME -n openshift-ingress --port http 
kubectl wait --for=condition=ready pod -l app.kubernetes.io/part-of=llminferenceservice -n $NS --timeout 150s
  
LB_HOST=$(kubectl get route/openshift-ai-inference-$GW_CLASS_NAME -n openshift-ingress -o=jsonpath='{.status.ingress[*].host}')

curl http://$LB_HOST/$NS/$LLM_ISVC_NAME/v1/completions  \
    -H "Content-Type: application/json" \
    -d '{
        "model":"'"$MODEL_ID"'",
        "prompt": "San Francisco is a"
    }'
```

*Using Port-forward*
```shell

kubectl port-forward svc/openshift-ai-inference-istio -n openshift-ingress  8001:80 &
curl -sS -X POST http://localhost:8001/$NS/$LLM_ISVC_NAME/v1/completions   \
    -H 'accept: application/json'   \
    -H 'Content-Type: application/json'    \
    -d '{
        "model":"'"$MODEL_ID"'",
        "prompt":"Who are you?"
      }'
```
