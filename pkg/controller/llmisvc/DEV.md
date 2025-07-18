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