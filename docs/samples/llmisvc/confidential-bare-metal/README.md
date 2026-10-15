# Confidential LLM serving on bare metal

Select `kserve-config-llm-confidential-bare-metal` from an ordinary RHOAI
project. The LLMISVC reconciler creates a shared helper ConfigMap, service
account, and SCC RoleBinding in that project, plus a separate encrypted raw
block PVC for each service. You do not need to apply these resources manually.

## Administrator: onboard the cluster once

Install and configure OSC's GPU confidential runtime, Trustee/KBS, attestation
policies/reference values, registry access and image verification. Provision
the LUKS passphrase and model decryption keys in KBS. Provide encrypted model
artifacts in the storage initializer's JWE format. Qualify a CSI StorageClass
that supports raw block devices and `ReadWriteOncePod` with the CoCo runtime.
LSO/manual `local` PVs do not meet this initial storage contract.

The ODH overlay installs the preset, dedicated SCC, SCC-use ClusterRole and
helper definitions. Edit [admin-profile.json](admin-profile.json) for your
installation, then update the existing ConfigMap (replace `opendatahub` with
your RHOAI applications namespace):

```sh
oc patch configmap inferenceservice-config -n opendatahub --type=merge -p "$(jq -n --rawfile profile admin-profile.json '{data:{confidentialBareMetal:$profile}}')"
```

The module operator preserves this ConfigMap key during reconciliation and
upgrades. There is no additional operator API or namespace approval list.
`storageSize` is the initial capacity, not an automatic resize request.
`luksResourceId` is a KBS reference; it is never a plaintext passphrase.
Optionally set `initData` to gzip/base64-encoded `initdata.toml`. It defaults to
an empty string. A service's explicit `spec.annotations` initdata value overrides
this default. If both are empty, configure OSC's global initdata before deployment.

The scripts under `config/overlays/odh/confidential/` are product resources:
`format-disk.sh` fetches the passphrase through guest-local CDH and starts the
OSC LUKS helper; `link.sh` exposes its unlocked filesystem to the initializer
and inference container. The overlay packages them for the reconciler to copy
into each serving project. Customers do not run these scripts themselves.

## Deploy a model

Edit [llminferenceservice.yaml](llminferenceservice.yaml): choose the encrypted
model URI, model decryption key reference, and resources appropriate for your
model. Guest RAM must also cover VM overhead. Supply model access through the
existing RHOAI connection/`serving.kserve.io/storageSecretName` mechanism. For
example, use this metadata annotation to select a connection Secret:

```yaml
metadata:
  annotations:
    serving.kserve.io/storageSecretName: my-model-connection
```

Apply the service in your project:

```sh
oc apply -n my-existing-project -f llminferenceservice.yaml
oc get llminferenceservice,pvc,pod -n my-existing-project
```

The pod uses a restartable `format-disk` sidecar, followed by `check-ready`,
then download and decryption in `storage-initializer`. Model data stays on the
LUKS filesystem; scratch paths use protected guest memory. Download protocols
are handled by the existing storage initializer, without a separate confidential
protocol whitelist. The preset defaults `runtimeClassName` to
`kata-cc-nvidia-gpu`; service overrides use normal config merging. Qualify any
alternative runtime against these helpers and the confidentiality requirements.

## Storage lifecycle and initial limits

Each service receives `<service-name>-confidential-storage`, using raw block
mode and `ReadWriteOncePod`. Its Deployment uses `Recreate` so replacements do
not concurrently write the filesystem. Pending claims are allowed while a
`WaitForFirstConsumer` StorageClass waits for the model pod.

An optional service metadata annotation
`serving.kserve.io/confidential-storage-size: 300Gi` overrides initial capacity.
To reuse an existing compatible raw block claim, explicitly select it:

```yaml
spec:
  template:
    volumes:
    - name: luks-block
      persistentVolumeClaim:
        claimName: retained-model-volume
```

PVCs remain after service deletion. Recreating a service with the same name
requires explicitly referencing its retained claim, including when its claim
name matches the automatic name. Compatible claims are reused without resizing
or reformatting. A key mismatch fails instead of wiping the LUKS header.
Namespace helpers remain shared across services for the project's lifetime.

The initial implementation supports one model pod per encrypted filesystem.
Extra replicas, worker pods, prefill/decode, LoRA adapters, and configured KV
cache offloading are unsupported. Ordinary caching in protected memory remains
available. Full attestation, GPU, download/decryption, inference and retained
volume replacement require verification on a supported bare-metal environment;
controller tests cannot establish those hardware properties.
