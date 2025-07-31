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
- Install istio operator (This is not needed when openshift has all capability of GIE)

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
  startingCSV: cert-manager-operator.v1.16.1
EOF

while [ $(kubectl get pod -n cert-manager-operator  | wc -l) -le 1 ]; 
do
  echo "⏳ waiting for Cert-Manager Pod to appear…"
  sleep 10
done
kubectl wait pod -l name=cert-manager-operator -n cert-manager-operator --for=condition=Ready --timeout=120s 
```

**Install LWS Operator**

This step should be changed when official lws-operator is released.

```shell
cat <<EOF | kubectl create -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: lws-operator
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: quay.io/jooholee/lws-operator-index:llmd
EOF

kubectl wait pod -l olm.catalogSource=lws-operator -n openshift-marketplace --for=condition=Ready --timeout=180s

kubectl create ns openshift-lws-operator || true

cat <<EOF | kubectl create -f -
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
EOF

kubectl wait pod -l name=openshift-lws-operator -n openshift-lws-operator --for=condition=Ready --timeout=120s

until kubectl get crd leaderworkersetoperators.operator.openshift.io &> /dev/null; do
  echo "⏳ waiting for CRD to appear…"
  sleep 2
done

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

**Install OSSM(DO NOT USE)**

> [!NOTE] 
> The OSSM prebuilt image have an issue so do not follow up this step for now(2025.July.30)
> USE `upstream istio` following the next step `Install upstream ISTIO(Optional)`

You have to add pullsecret for brew image on your cluster.

```shell

# Update PULL SECRET
export BREW_PULL_SECRET_FILE="path/to/file"
export REGISTRY_PULL_SECRET_FILE="path/to/file"

kubectl get secret pull-secret -n openshift-config -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d > /tmp/pull-secret.json 
jq -s '.[0].auths += .[1].auths | {auths: .[0].auths}' /tmp/pull-secret.json $BREW_PULL_SECRET_FILE > /tmp/new-pull-secret.json    
jq -s '.[0].auths += .[1].auths | {auths: .[0].auths}' /tmp/new-pull-secret.json  $REGISTRY_PULL_SECRET_FILE > /tmp/final-pull-secret.json    
kubectl set data secret/pull-secret -n openshift-config --from-file=.dockerconfigjson=/tmp/final-pull-secret.json  

# Create MirrorSet to pull prebuilt images 
cat <<EOF| kubectl create -f -
apiVersion: config.openshift.io/v1
kind: ImageTagMirrorSet
metadata:
    name: stage-registry
spec:
    imageTagMirrors:
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh
          source: registry.redhat.io/openshift-service-mesh
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh-tech-preview
          source: registry.redhat.io/openshift-service-mesh-tech-preview
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh-dev-preview-beta
          source: registry.redhat.io/openshift-service-mesh-dev-preview-beta
---
apiVersion: config.openshift.io/v1
kind: ImageDigestMirrorSet
metadata:
    name: stage-registry
spec:
    imageDigestMirrors:
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh
          source: registry.redhat.io/openshift-service-mesh
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh-tech-preview
          source: registry.redhat.io/openshift-service-mesh-tech-preview
        - mirrors:
            - registry.stage.redhat.io/openshift-service-mesh-dev-preview-beta
          source: registry.redhat.io/openshift-service-mesh-dev-preview-beta
EOF

# Deploy OSSM  (need to update iib image when blocker issue is fixed)
cat<<EOF |kubectl create -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: istio-catalog
  namespace: openshift-marketplace
spec:
  displayName: istio-catalog
  image: brew.registry.redhat.io/rh-osbs/iib:1015285
  publisher: grpc
  sourceType: grpc
EOF

cat<<EOF|kubectl create -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: servicemeshoperator3
  namespace: openshift-operators
spec:
  channel: stable
  installPlanApproval: Automatic
  name: servicemeshoperator3
  source: istio-catalog
  sourceNamespace: openshift-marketplace
  startingCSV: servicemeshoperator3.v3.1.0
EOF

kubectl wait --for=condition=ready pod -l control-plane=servicemesh-operator3 -n openshift-operators --timeout=300s


kubectl create ns istio-cni  
cat<<EOF|kubectl create -f -
kind: IstioCNI
apiVersion: sailoperator.io/v1
metadata:
  name: default
spec:
  namespace: istio-cni
  version: v1.26.2
EOF

kubectl create ns istio-system

cat<<EOF|kubectl create -f -
apiVersion: sailoperator.io/v1
kind: Istio
metadata:
  name: default
spec:
  namespace: istio-system
  updateStrategy:
    type: InPlace
    inactiveRevisionDeletionGracePeriodSeconds: 30
  version: v1.26.2
  values:
    pilot:
      env:
        SUPPORT_GATEWAY_API_INFERENCE_EXTENSION: "true"    # TO-DO: This need to be removed soon [istio issue](https://github.com/istio/istio/pull/57099)
        ENABLE_GATEWAY_API_INFERENCE_EXTENSION: "true"
EOF
```
**Install upstream ISTIO(Optional)**

This step will be removed at some point because the ISTIO(OSSM) should be provided by the platform.

```shell
kubectl create ns istio-system || true
kubectl create -f test/overlays/llm-istio-experimental -n istio-system
```

**Create a gateway**
```shell
INGRESS_NS=openshift-ingress
kubectl create namespace ${INGRESS_NS} || true

kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: openshift-ai-inference
  namespace: openshift-ingress
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
```

**Deploy Kserve using overlay/odh**

A new CRD related objects will be added 
  - LLMIsvc/LLMIsvcConfig CRD
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

kubectl kustomize config/overlays/odh | kubectl apply  --server-side=true -f -

kubectl wait --for=condition=ready pod -l control-plane=kserve-controller-manager -n opendatahub  --timeout=300s
```

Deploy the model:

```shell
NS=llm-test
LLM_ISVC=docs/samples/llmisvc/opt-125m/llm-inference-service-facebook-opt-125m-cpu.yaml
LLM_ISVC_NAME=$(cat $LLM_ISVC | yq .metadata.name)

kubectl get ns $NS||kubectl create ns $NS
kubectl apply -n ${NS} -f ${LLM_ISVC}
```


#### Validation

**ROSA Cluster**
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

oc expose svc/openshift-ai-inference-istio -n openshift-ingress --port http 
kubectl wait --for=condition=ready pod -l app.kubernetes.io/part-of=llminferenceservice -n $NS --timeout 150s
  
LB_HOST=$( kubectl get route/openshift-ai-inference-istio -n openshift-ingress -o=jsonpath='{.status.ingress[*].host}'  )

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