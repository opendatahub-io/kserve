# Confidential LLM serving on bare metal

Select `kserve-config-llm-confidential-bare-metal` from an ordinary RHOAI
project. The LLMISVC controller creates the shared helper ConfigMap, service
account, and SCC RoleBinding needed by the confidential pod. The customer must
provide storage and select its existing PVC in the LLMInferenceService.

## Administrator: prepare the runtime, keys, and storage

Install and configure OSC's GPU confidential runtime, Trustee/KBS, attestation
policies/reference values, registry access, and image verification. Provide the
disk passphrase and model decryption keys in KBS, and encrypted model artifacts
in the storage initializer's JWE format.

Create suitable storage before deploying the model. Its PVC must be in the
model's namespace, writable, and use `volumeMode: Block`. Supported layouts are
a local block PV with `ReadWriteOnce`, or CSI block storage with
`ReadWriteOncePod`. A local PV needs the correct device path and node affinity.
A StorageClass is not required for an existing classless local PV/PVC pair.
Verify that its node also has the GPU and confidential runtime the model needs.

The customer is responsible for capacity, storage availability, access control,
retention, and recovery. Dedicate each PVC and its disk to one model service.
KServe does not check whether another application uses that disk. RWO can allow
several pods on the same node to write the disk; CSI RWOP restricts access to one
pod. Do not move the disk while an old guest might still be writing.

The ODH overlay installs the preset, dedicated SCC, SCC-use ClusterRole, and
helper definitions. Edit [admin-profile.json](admin-profile.json) with the actual
disk-key reference, then update the existing ConfigMap (replace `opendatahub`
with your RHOAI applications namespace):

```sh
oc patch configmap inferenceservice-config -n opendatahub --type=merge -p "$(jq -n --rawfile profile admin-profile.json '{data:{confidentialBareMetal:$profile}}')"
```

The module operator preserves this ConfigMap key during reconciliation and
upgrades. `luksResourceId` identifies the disk key; it is never the plaintext
passphrase and must match the selected disk. The model's
`model.confidential.resourceId` separately identifies the encrypted artifact's
key. No StorageClass, creation size, or device inventory is configured in this
entry. Previously configured allocation settings are ignored.

Optionally set `initData` to gzip/base64-encoded `initdata.toml`. A service's
explicit `spec.annotations` initdata value overrides this default. If both are
empty, configure OSC's global initdata before deployment.

## Deploy a model using its existing PVC

Edit [llminferenceservice.yaml](llminferenceservice.yaml): choose the encrypted
model URI, model key reference, resources, and the existing claim name. Add this
selection under `spec.template`, alongside any containers and init containers:

```yaml
spec:
  template:
    volumes:
    - name: luks-block
      persistentVolumeClaim:
        claimName: my-existing-model-disk
```

The volume name `luks-block` connects the supplied claim to the preset's disk
helper. The model selects a PVC; that PVC's binding identifies its PV. The
installed preset does not generate or select a claim name.

Supply model access through the existing RHOAI connection mechanism, for example:

```yaml
metadata:
  annotations:
    serving.kserve.io/storageSecretName: my-model-connection
```

Guest RAM must cover model requirements and VM overhead. Apply the service in
the namespace containing the claim:

```sh
oc apply -n my-existing-project -f llminferenceservice.yaml
oc get llminferenceservice,pvc,pod -n my-existing-project
```

A missing PVC, read-only volume reference, or filesystem-mode claim produces an
error. Once the selected block PVC exists, KServe can create the model
Deployment. Kubernetes handles PV/PVC binding and scheduling, and keeps the pod
pending until storage is ready. KServe does not inspect PVs or modify storage
objects.

## Guest encryption and model startup

The pod uses a restartable `format-disk` helper, followed by `check-ready`, then
download and decryption in `storage-initializer`. The guest obtains its disk key
through the attestation flow. The existing helper can initialize a blank device
or use an existing LUKS device. A key mismatch or incompatible disk signature
fails without wiping the disk. Supplying a PV/PVC does not require customers to
run these guest scripts themselves.

The scripts under `config/overlays/odh/confidential/` are packaged product
resources. `format-disk.sh` obtains the passphrase through guest-local CDH and
starts the OSC LUKS helper; `link.sh` exposes the unlocked filesystem to the
initializer and inference container. Model data stays on LUKS storage; scratch
paths use protected guest memory.

The preset requires `runtimeClassName: kata-cc-nvidia-gpu`. KServe rejects other
runtime classes before creating the namespace's confidential helper permissions.
Download protocols are handled by the existing storage initializer.

## Replacement, deletion, and limits

The Deployment uses `Recreate` so normal replacements stop the old model pod
before creating the new one. Deleting the service leaves the customer-owned PV
and PVC untouched. Customers decide whether to reuse, recover, or delete them,
and should choose an appropriate PV reclaim policy themselves. Namespace
helpers remain shared across services for the project's lifetime.

The implementation supports one model pod per encrypted filesystem. Extra
replicas, worker pods, prefill/decode, LoRA adapters, and configured KV cache
offloading are unsupported. Ordinary caching in protected memory remains
available. Attestation, GPU access, disk-key release, existing-data preservation,
inference, and model replacement need verification on supported bare-metal
hardware; controller tests cannot establish those properties.
