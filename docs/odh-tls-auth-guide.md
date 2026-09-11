# Enabling Authentication and TLS for KServe InferenceServices on OpenDataHub

This guide explains how to enable authentication (auth) and TLS encryption for
KServe InferenceServices deployed in **Standard (raw deployment)** mode on
OpenShift with OpenDataHub.

## How It Works

When you enable auth on an InferenceService, the KServe controller automatically:

1. **Injects a `kube-rbac-proxy` sidecar** into the predictor deployment that
   terminates TLS and enforces Kubernetes RBAC authorization on every request.
2. **Provisions TLS certificates automatically** via the OpenShift service-ca
   controller -- no manual certificate management required.
3. **Configures the transformer** (if present) with its own TLS serving
   certificate and CA trust bundle so it can communicate securely with the
   predictor over HTTPS.

### Certificate Lifecycle

All certificates are managed automatically by OpenShift:

- The KServe controller annotates each Service with
  `service.beta.openshift.io/serving-cert-secret-name`, which tells the
  OpenShift service-ca controller to create a TLS Secret
  (`<service-name>-serving-cert`) containing `tls.crt` and `tls.key`.
- The service-ca controller also maintains a ConfigMap
  (`openshift-service-ca.crt`) with the CA bundle that can verify those
  certificates.
- Certificates are automatically rotated by OpenShift before expiry.

You do not need to create, mount, or rotate any certificates yourself.

## Prerequisites

- OpenShift cluster with OpenDataHub operator installed
- KServe configured in **Standard** deployment mode
- A namespace with a ServingRuntime available

## Enabling Auth on an InferenceService

Add two items to your InferenceService manifest:

1. The annotation `security.opendatahub.io/enable-auth: "true"`
2. The label `networking.kserve.io/visibility: exposed` (if you want an
   external Route)

### Example: Predictor Only

```yaml
apiVersion: serving.kserve.io/v1beta1
kind: InferenceService
metadata:
  name: my-model
  namespace: my-namespace
  annotations:
    security.opendatahub.io/enable-auth: "true"
    serving.kserve.io/deploymentMode: Standard
  labels:
    networking.kserve.io/visibility: exposed
spec:
  predictor:
    model:
      modelFormat:
        name: onnx
      runtime: my-runtime
      storageUri: s3://my-bucket/my-model
```

### Example: Predictor + Transformer

```yaml
apiVersion: serving.kserve.io/v1beta1
kind: InferenceService
metadata:
  name: sentiment-analysis
  namespace: my-namespace
  annotations:
    security.opendatahub.io/enable-auth: "true"
    serving.kserve.io/deploymentMode: Standard
  labels:
    networking.kserve.io/visibility: exposed
spec:
  predictor:
    model:
      modelFormat:
        name: onnx
      runtime: mlserver-runtime
      storageUri: hf://optimum/distilbert-base-uncased-finetuned-sst-2-english
  transformer:
    containers:
      - name: kserve-container
        image: quay.io/my-org/my-custom-transformer:latest
        args:
          - --model-name=sentiment-analysis
          - --tokenizer_name=optimum/distilbert-base-uncased-finetuned-sst-2-english
```

## What Gets Created

When auth is enabled, the controller sets up the following for each component:

### Predictor

| Resource | Purpose |
|----------|---------|
| `kube-rbac-proxy` sidecar container | Terminates TLS on port 8443, enforces RBAC |
| `<name>-predictor-serving-cert` Secret | TLS certificate (auto-provisioned) |
| `<name>-kube-rbac-proxy-sar-config` ConfigMap | SubjectAccessReview policy |
| Service port override to 8443 | Routes traffic through the auth proxy |

### Transformer (when present)

| Resource | Purpose |
|----------|---------|
| `<name>-transformer-serving-cert` Secret | Transformer's own TLS certificate |
| `openshift-service-ca.crt` ConfigMap volume | CA bundle to verify predictor's cert |
| `SSL_CERT_DIR` env var | Points Python's SSL to the CA bundle |
| `PREDICTOR_HOST` / `PREDICTOR_PORT` env vars | Predictor endpoint discovery |
| `--predictor_use_ssl true` arg | Tells the Python SDK to use HTTPS |
| `--http_port 8443` override | Transformer listens on HTTPS port |

The transformer does **not** get the `kube-rbac-proxy` sidecar -- only the
predictor has the auth proxy. The transformer communicates directly with the
predictor's auth proxy over TLS.

## Sending Requests

### Get an Auth Token

Since auth is enforced via Kubernetes RBAC, you need a ServiceAccount token
with permission to `get` the InferenceService resource:

```bash
# Create a ServiceAccount (or use an existing one)
oc create sa my-client -n my-namespace

# Grant it permission to access the InferenceService
oc create role isvc-reader \
  --verb=get \
  --resource=inferenceservices.serving.kserve.io \
  --resource-name=sentiment-analysis \
  -n my-namespace

oc create rolebinding my-client-isvc-reader \
  --role=isvc-reader \
  --serviceaccount=my-namespace:my-client \
  -n my-namespace

# Get a token
TOKEN=$(oc create token my-client -n my-namespace)
```

### Send a Request via the Route

```bash
# Get the route URL
URL=$(oc get route -n my-namespace -l serving.kserve.io/inferenceservice=sentiment-analysis \
  -o jsonpath='{.items[0].status.ingress[0].host}')

curl -sk "https://${URL}/v1/models/sentiment-analysis:predict" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"texts": ["This product exceeded my expectations", "Worst purchase ever total waste of money"]}'
```

Example response:

```json
{
  "predictions": [
    {
      "sentiment": "positive",
      "confidence": 0.9989,
      "all_scores": { "negative": 0.0011, "positive": 0.9989 },
      "star_rating": 5
    },
    {
      "sentiment": "negative",
      "confidence": 0.9998,
      "all_scores": { "negative": 0.9998, "positive": 0.0002 },
      "star_rating": 1
    }
  ]
}
```

### Send a Request Cluster-Internally

From within a pod in the cluster, you can call the transformer service
directly over HTTPS:

```bash
curl -sk "https://sentiment-analysis-transformer.my-namespace.svc:8443/v1/models/sentiment-analysis:predict" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"texts": ["This product exceeded my expectations"]}'
```

## Disabling Auth

Remove the annotation or set it to `"false"`:

```bash
oc annotate isvc sentiment-analysis security.opendatahub.io/enable-auth- -n my-namespace
```

On the next reconciliation the auth proxy sidecar will be removed and the
service will revert to plain HTTP on port 80.

## Troubleshooting

### Pod not starting (CrashLoopBackOff)

Check if the serving-cert Secret exists:

```bash
oc get secret <name>-predictor-serving-cert -n my-namespace
```

If missing, verify the Service has the annotation:

```bash
oc get svc <name>-predictor -n my-namespace -o jsonpath='{.metadata.annotations}'
```

The annotation `service.beta.openshift.io/serving-cert-secret-name` must be
present. The OpenShift service-ca controller creates the Secret from this
annotation.

### 403 Forbidden responses

The token's ServiceAccount lacks permission. Verify the RoleBinding:

```bash
oc auth can-i get inferenceservices.serving.kserve.io/<name> \
  --as=system:serviceaccount:my-namespace:my-client -n my-namespace
```

### Transformer cannot reach predictor

Check the transformer pod logs for TLS errors. Verify the CA bundle is mounted:

```bash
oc exec deploy/<name>-transformer -c kserve-container -- \
  ls /etc/odh/openshift-service-ca-bundle/
```

You should see `service-ca.crt`. If missing, ensure the `openshift-service-ca.crt`
ConfigMap exists in the namespace:

```bash
oc get cm openshift-service-ca.crt -n my-namespace
```

### Probe failures on transformer

The readiness probe must target port 8443 (not 8080) when auth is enabled.
Verify:

```bash
oc get deploy <name>-transformer -n my-namespace \
  -o jsonpath='{.spec.template.spec.containers[0].readinessProbe.tcpSocket.port}'
```

Expected output: `8443`