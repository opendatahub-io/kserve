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
helper definitions. Configure the fallback disk-key reference on the
cluster-wide `Kserve` resource:

```yaml
apiVersion: components.platform.opendatahub.io/v1alpha1
kind: Kserve
metadata:
  name: default-kserve
spec:
  managementState: Managed
  confidentialContainers:
    luksResourceId: kbs:///default/kserve-luks-password/key
    initData: ""
```

Merge these settings into your existing `Kserve/default-kserve` configuration.
For example, to set the disk-key reference:

```sh
oc patch kserve default-kserve --type=merge -p '{"spec":{"confidentialContainers":{"luksResourceId":"kbs:///default/kserve-luks-password/key"}}}'
```

The module operator writes these settings into `inferenceservice-config` and
keeps them in sync with the `Kserve` resource. Configure this API instead of
editing the ConfigMap directly; manual ConfigMap edits are overwritten. If you
previously edited the ConfigMap, copy those values into the `Kserve` resource
before upgrading.

`luksResourceId` identifies the disk key; it is never the plaintext passphrase
and must match the selected disk. It is used when a service does not supply its
own `LUKS_RESOURCE_ID`. The installation setting defaults to
`kbs:///default/kserve-luks-password/key` when omitted. The model's
`model.confidential.resourceId` separately identifies the encrypted artifact's
key. Storage capacity and the PVC selection remain the customer's responsibility.

Optionally set `spec.confidentialContainers.initData` to gzip/base64-encoded
`initdata.toml`. A service's
explicit `spec.annotations` initdata value overrides this default. If both are
empty, configure OSC's global initdata before deployment.

Removing `spec.confidentialContainers` restores the shipped defaults: the
default disk-key reference and no installation-specific init data.

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

To use a different disk key for this service, set `LUKS_RESOURCE_ID` on its
`format-disk` init container:

```yaml
spec:
  template:
    initContainers:
    - name: format-disk
      env:
      - name: LUKS_RESOURCE_ID
        value: kbs:///customer/model-disk/key
```

This service setting takes precedence over
`Kserve.spec.confidentialContainers.luksResourceId`. If you omit the environment
variable, the installation setting is used, falling back to
`kbs:///default/kserve-luks-password/key`. An explicitly empty or invalid value
produces an error. Each model can use a separate disk and key; an existing LUKS
disk still requires the key it was encrypted with. Changing this reference does
not change the disk's encryption key. The separate
`spec.model.confidential.resourceId` continues to select the model artifact's
decryption key.

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
