### Local Dev

#### Deploying LLMInferenceService controller locally

> [!IMPORTANT]
> If you are using quay.io make sure to change kserve binary img repos visibility to public!

##### Using `kind`

```shell
kind create cluster -n "kserve-llm-d"

go install sigs.k8s.io/cloud-provider-kind@latest

cloud-provider-kind> /dev/null 2>&1 &
```

##### Using `minikube`

```shell
minikube start --cpus='12' --memory='16G'
minikube addons enable metallb

# You need to configure metallb with an IP range. This depends on the minikube network.
# You can find your current minikube ip with:
# $ minikube ip
#   192.168.39.118
#
# With the previous sample output, you would configure metallb with a range not including
# the minikube IP (change only the last entry). E.g:
minikube addons configure metallb
# Minikube will ask two prompts. Notice the configured range 192.168.39.200-192.168.39.235 is
# not including minikube IP:
# -- Enter Load Balancer Start IP: 192.168.39.200
# -- Enter Load Balancer End IP: 192.168.39.235
```

#### Install KServe (dev) in the created cluster

```shell
make deploy-dev-llm -e KO_DOCKER_REPO=<YOUR_REPO>
```

#### Creating simple CPU model

```shell
NS=llm-test
kubectl create namespace ${NS} || true

kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: kserve-ingress-gateway
  namespace: kserve
spec:
  gatewayClassName: istio
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

LLM_ISVC=docs/samples/llmisvc/opt-125m/llm-inference-service-facebook-opt-125m-cpu.yaml
LLM_ISVC_NAME=$(cat $LLM_ISVC | yq .metadata.name)

kubectl apply -n ${NS} -f ${LLM_ISVC}
```

#### Validation

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
  "prompt": ""Delve into the multifaceted implications of a fully disaggregated cloud architecture, specifically where the compute plane (P) and the data plane (D) are independently deployed and managed for a geographically distributed, high-throughput, low-latency microservices ecosystem. Beyond the fundamental challenges of network latency and data consistency, elaborate on the advanced considerations and trade-offs inherent in such a setup: 1. Network Architecture and Protocols: How would the network fabric and underlying protocols (e.g., RDMA, custom transport layers) need to evolve to support optimal performance and minimize inter-plane communication overhead, especially for synchronous operations? Discuss the role of network programmability (e.g., SDN, P4) in dynamically optimizing routing and traffic flow between P and D. 2. Advanced Data Consistency and Durability: Explore sophisticated data consistency models (e.g., causal consistency, strong eventual consistency) and their applicability in balancing performance and data integrity across a globally distributed data plane. Detail strategies for ensuring data durability and fault tolerance, including multi-region replication, intelligent partitioning, and recovery mechanisms in the event of partial or full plane failures. 3. Dynamic Resource Orchestration and Cost Optimization: Analyze how an orchestration layer would intelligently manage the independent scaling of compute (P) and data (D) resources, considering fluctuating workloads, cost efficiency, and performance targets (e.g., using predictive analytics for resource provisioning). Discuss mechanisms for dynamically reallocating compute nodes to different data partitions based on workload patterns and data locality, potentially involving live migration strategies. 4. Security and Compliance in a Distributed Landscape: Address the enhanced security perimeter challenges, including securing communication channels between P and D (encryption in transit, mutual TLS), fine-grained access control to data at rest and in motion, and identity management across disaggregated components. Discuss how such an architecture impacts compliance with regulatory frameworks (e.g., GDPR, HIPAA) concerning data sovereignty, privacy, and auditability. 5. Operational Complexity and Observability: Examine the increased complexity in monitoring, logging, and tracing across highly decoupled compute and data planes. What specialized tooling and practices (e.g., distributed tracing with OpenTelemetry, advanced AIOps) would be essential? How would incident response and troubleshooting differ in this disaggregated environment compared to traditional integrated systems? Consider the challenges of pinpointing root causes across independent failures. 6. Real-world Applicability and Future Trends: Identify specific industries or use cases (e.g., high-frequency trading, IoT edge processing, large language model inference) where the benefits of P/D disaggregation would strongly outweigh its complexities. Conclude by speculating on emerging technologies or paradigms (e.g., serverless compute functions directly interacting with object storage, in-memory disaggregation) that could further drive or transform P/D disaggregation in cloud computing.", 
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

##### Using `openshift local`

```shell
crc setup
crc config set memory 16384
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
- Install istio operator (This is not needed when openshift has all capability of GIE)

**Install Cert-Manager**

```shell
oc new-project cert-manager-operator

cat<<EOF |oc create -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-cert-manager-operator
  namespace: cert-manager-operator
spec:
  targetNamespaces: []
  spec: {}
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: openshift-cert-manager-operator
  namespace: cert-manager-operator
spec:
  channel: stable-v1
  name: openshift-cert-manager-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
EOF
```

**Install LWS Operator**

This step should be changed when official lws-operator is released.

```shell
cat <<EOF|oc create -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: lws-operator
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: quay.io/jooholee/lws-operator-index:llmd
EOF

oc wait pod -l olm.catalogSource=lws-operator -n openshift-marketplace --for=condition=Ready --timeout=120s

oc create ns openshift-lws-operator

cat <<EOF|oc create -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-lws-operator-jw944
  namespace: openshift-lws-operator
spec:
  targetNamespaces:
  - openshift-lws-operator
  upgradeStrategy: Default
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: leader-worker-set
  namespace: openshift-lws-operator
spec:
  channel: stable
  installPlanApproval: Automatic
  name: leader-worker-set
  source: lws-operator
  sourceNamespace: openshift-marketplace
  startingCSV: leader-worker-set.v1.0.0

oc wait pod -l name=openshift-lws-operator -n openshift-lws-operator --for=condition=Ready --timeout=120s

cat <<EOF|oc create -f -
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

**Install GATEWAY API CRD**
This step will be removed at some point because the CRD should be provided by the platform.

```shell
GATEWAY_API_EXPERIMENTAL_VERSION="v1.3.0"
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API_EXPERIMENTAL_VERSION/experimental-install.yaml
```

**Install ISTIO**

This step will be removed at some point because the ISTIO(OSSM) should be provided by the platform.

```shell
oc create ns istio-system
oc create -f test/overlays/llm-istio-experimental -n istio-system

cat <<EOF| oc create -f -
apiVersion: gateway.networking.k8s.io/v1beta1
kind: GatewayClass
metadata:
  name: istio
spec:
  controllerName: istio.io/gateway-controller
  description: "Custom Istio GatewayClass"
EOF  
```

**Deploy Kserve using overlay/odh**

A new CRD related objects will be added 
  - LLMIsvc/LLMIsvcConfig CRD
  - Webhook
  - `well-know preset` LlmIsvcConfig in the controller namespace

```shell
# Create controller namespace
oc get ns opendatahub || oc new-project opendatahub

# Create CRDs
kustomize build config/crd | oc apply --server-side=true -f -

oc wait --for=condition=Established --timeout=60s crd/llminferenceserviceconfigs.serving.kserve.io

# Deploy Kserve
kustomize build config/overlays/odh |oc apply  --server-side=true -f -

oc wait --for=condition=ready pod -l control-plane=kserve-controller-manager -n opendatahub  --timeout=300s

# Retry to create kserve again for preset LLMInferenceserviceConfigs
kustomize build config/overlays/odh |oc apply  --server-side=true -f -
```

Deploy the model:

```shell
LLM_ISVC=docs/samples/llmisvc/opt-125m/llm-inference-service-facebook-opt-125m-cpu.yaml
LLM_ISVC_NAME=$(cat $LLM_ISVC | yq .metadata.name)

kubectl apply -n ${NS} -f ${LLM_ISVC}
```


#### Validation

```shell
LB_IP=$(kubectl get svc/kserve-ingress-gateway-istio -n opendatahub -o=jsonpath='{.status.loadBalancer.ingress[0].hostname}')

curl http://${LB_IP}/${NS}/${LLM_ISVC_NAME}/v1/completions  \
    -H "Content-Type: application/json" \
    -d '{
        "model": "facebook/opt-125m",
        "prompt": "San Francisco is a"
    }'
```