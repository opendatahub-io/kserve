# LLM Inference Service Samples

This directory contains sample configurations and guides for deploying LLM Inference Services on OpenShift Container
Platform (OCP) **4.19.9+**.

> [!IMPORTANT]
> **Hardware-Specific Configuration Required**
>
> The configurations in this guide and the sample YAML files are **hardware-specific examples**. You must adjust them
> based on your cluster's actual hardware:
> - **Network Interfaces**: NIC vendor IDs, device IDs, PCI addresses, and interface names
> - **GPU Configuration**: GPU model, count, and node selectors
> - **RDMA Settings**: Network topology and RDMA capabilities
> - **Resource Allocations**: Virtual functions, memory, and compute resources
>
> Use the provided examples as templates and modify them according to your hardware discovered through
> `SriovNetworkNodeState`, `oc get nodes --show-labels`, and hardware inspection tools.

## Table of Contents

- [OpenShift Setup Requirements](#openshift-setup-requirements)
- [Installing SR-IOV Network Operator](#installing-sr-iov-network-operator)
- [Installing NVIDIA GPU Operator](#installing-nvidia-gpu-operator)
- [Deployment Guides](#deployment-guides)
- [Sample Configurations](#sample-configurations)

## OpenShift Setup Requirements

To run distributed inference workloads with disaggregated Prefill/Decode serving (P/D) and Data+Expert Parallelism
(DP+EP), you need to configure your OpenShift cluster with:

1. **SR-IOV Network Operator** - For high-performance networking with RDMA support
2. **NVIDIA GPU Operator** - For GPU access with RDMA and specific kernel parameters enabled

## Installing SR-IOV Network Operator

The SR-IOV Network Operator enables high-performance networking capabilities required for distributed inference
workloads, particularly for RDMA over Converged Ethernet (RoCE).

### Official Documentation

For detailed installation instructions, refer to the official OpenShift documentation:

- [Configuring hardware networks](https://docs.redhat.com/en/documentation/openshift_container_platform/4.19/html-single/hardware_networks/index)

### Quick Installation

1. **Install the SR-IOV Network Operator**

Create the operator subscription:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: openshift-sriov-network-operator

---
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: sriov-network-operators
  namespace: openshift-sriov-network-operator
spec:
  targetNamespaces:
    - openshift-sriov-network-operator

---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: sriov-network-operator-subscription
  namespace: openshift-sriov-network-operator
spec:
  channel: "stable"
  installPlanApproval: Automatic
  name: sriov-network-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
```

Apply the configuration:

```bash
oc apply -f sriov-operator.yaml
```

Wait for the SR-IOV Network Operator to be ready:

```bash
# Wait for the subscription to reach AtLatestKnown state
oc wait --timeout=8m --for jsonpath='{.status.state}'=AtLatestKnown \
  -n openshift-sriov-network-operator subscription/sriov-network-operator-subscription

# Verify the operator pods are running
oc get pods -n openshift-sriov-network-operator
```

2. **Configure SR-IOV Network Node Policy**

After the operator is installed, configure the SR-IOV network policy. This configuration is **cluster and hardware
dependent**.

First, check the `SriovNetworkNodeState` object to identify your hardware-specific configuration:

```bash
# List available nodes with SR-IOV capable devices
oc get sriovnetworknodestates -n openshift-sriov-network-operator

# Get detailed information about a specific node's network interfaces
oc get sriovnetworknodestate <node-name> -n openshift-sriov-network-operator -o yaml
```

The `SriovNetworkNodeState` output will show you the available network interfaces, their PCI addresses, device IDs,
vendor IDs, and capabilities. Use this information to configure your `SriovNetworkNodePolicy`.

Here's an example for Mellanox/NVIDIA ConnectX NICs:

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovNetworkNodePolicy
metadata:
  name: sriov-p2-policy
  namespace: openshift-sriov-network-operator
spec:
  deviceType: netdevice
  isRdma: true
  linkType: eth
  mtu: 9000
  nicSelector:
    deviceID: 101d           # Adjust based on your hardware
    rootDevices:
      - 0000:5c:00.0         # Adjust based on your hardware
      - 0000:5c:00.1         # Adjust based on your hardware
    pfNames:
      - p2                   # Adjust based on your hardware
    vendor: 15b3             # Mellanox/NVIDIA vendor ID
  nodeSelector:
    feature.node.kubernetes.io/pci-15b3.present: 'true'
    feature.node.kubernetes.io/pci-15b3.sriov.capable: 'true'
    feature.node.kubernetes.io/rdma.available: 'true'
    feature.node.kubernetes.io/rdma.capable: 'true'
  numVfs: 8
  priority: 98
  resourceName: p2rdma
```

> [!WARNING]
> After applying the `SriovNetworkNodePolicy`, the affected nodes will be drained, rebooted, and reconfigured. This
> process can take several minutes.

Apply the policy:

```bash
oc apply -f sriov-network-node-policy.yaml
```

```bash
# Monitor the node state synchronization
oc get sriovnetworknodestates -n openshift-sriov-network-operator

# Check that syncStatus is "Succeeded" for all nodes
oc get sriovnetworknodestate <node-name> -n openshift-sriov-network-operator -o jsonpath='{.status.syncStatus}'
```

3. **Create SR-IOV Network**

```yaml
apiVersion: sriovnetwork.openshift.io/v1
kind: SriovNetwork
metadata:
  name: roce-p2
  namespace: openshift-sriov-network-operator
spec:
  ipam: |-
    {
      "type": "whereabouts",
      "range": "10.0.132.0/24"
    }
  logLevel: info
  networkNamespace: 'llm-test'    # Adjust to your namespace
  resourceName: p2rdma
  spoofChk: "off"
  trust: "on"
  linkState: "enable"
```

> [!NOTE]
> - Identify your NIC device IDs and PCI addresses from the `SriovNetworkNodeState` output
> - Adjust `deviceID`, `rootDevices`, `pfNames`, and `vendor` based on your hardware
> - Ensure your nodes have the appropriate feature labels for RDMA support

## Installing NVIDIA GPU Operator

The NVIDIA GPU Operator manages the GPU software stack on OpenShift, including drivers, runtime, and device plugins.

### Important: Enable RDMA Support

When installing the GPU Operator for distributed inference workloads, **you must enable RDMA support** in the
ClusterPolicy configuration.

### Installation Steps

1. **Install the NVIDIA GPU Operator**

Create the operator subscription:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: nvidia-gpu-operator

---
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: nvidia-gpu-operator-group
  namespace: nvidia-gpu-operator
spec:
  targetNamespaces:
    - nvidia-gpu-operator

---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: gpu-operator-certified
  namespace: nvidia-gpu-operator
spec:
  channel: "stable"
  installPlanApproval: Automatic
  name: gpu-operator-certified
  source: certified-operators
  sourceNamespace: openshift-marketplace
```

Apply the configuration:

```bash
oc apply -f gpu-operator.yaml
```

Wait for the NVIDIA GPU Operator to be ready:

```bash
# Wait for the subscription to reach AtLatestKnown state
oc wait --timeout=3m --for jsonpath='{.status.state}'=AtLatestKnown \
  -n nvidia-gpu-operator subscription/gpu-operator-certified

# Wait for the install plan to complete
oc wait --timeout=3m --for condition=Installed -n nvidia-gpu-operator installplan --all

# Verify the operator deployment is ready
oc rollout status --watch --timeout=3m -n nvidia-gpu-operator deploy/gpu-operator
```

2. **Configure ClusterPolicy with RDMA and Kernel Parameters**

> [!IMPORTANT]
> The ClusterPolicy must be configured with:
> - **RDMA enabled** (`rdma.enabled: true`)
> - **Kernel module parameters** for GPU peer-to-peer communication

Create a ConfigMap with kernel module parameters:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kernel-module-params
  namespace: nvidia-gpu-operator
data:
  nvidia.conf: |-
    NVreg_RegistryDwords="PeerMappingOverride=1;"
    NVreg_EnableStreamMemOPs=1
```

Create the ClusterPolicy with RDMA enabled:

```yaml
apiVersion: nvidia.com/v1
kind: ClusterPolicy
metadata:
  name: gpu-cluster-policy
spec:
  daemonsets:
    rollingUpdate:
      maxUnavailable: "1"
    updateStrategy: RollingUpdate
  dcgm:
    enabled: true
  dcgmExporter:
    enabled: true
    serviceMonitor:
      enabled: true
  devicePlugin:
    enabled: true
  driver:
    enabled: true
    kernelModuleConfig:
      name: kernel-module-params    # Reference the ConfigMap created above
    rdma:
      enabled: true                  # CRITICAL: Enable RDMA support
      useHostMofed: false
    upgradePolicy:
      autoUpgrade: true
  gdrcopy:
    enabled: true
  gfd:
    enabled: true
  migManager:
    enabled: true
  nodeStatusExporter:
    enabled: true
  operator:
    defaultRuntime: crio
    runtimeClass: nvidia
    use_ocp_driver_toolkit: true
  toolkit:
    enabled: true
    installDir: /usr/local/nvidia
  vfioManager:
    enabled: true
```

**Key Configuration Points:**

- `driver.rdma.enabled: true` - Enables RDMA support for GPU Direct
- `driver.kernelModuleConfig.name: kernel-module-params` - References the kernel parameters ConfigMap
- `NVreg_RegistryDwords="PeerMappingOverride=1;"` - Enables GPU peer-to-peer mapping
- `NVreg_EnableStreamMemOPs=1` - Enables stream memory operations for better performance

> [!WARNING]
> After applying the ClusterPolicy, the GPU Operator will deploy multiple daemonsets across GPU nodes. This process
> includes building GPU drivers and can take 10-20 minutes.

Apply both configurations:

```bash
oc apply -f kernel-module-params.yaml
oc apply -f cluster-policy.yaml
```

3. **Verify Installation**

Wait for all components to be ready:

```bash
# Wait for critical GPU operator daemonsets to be ready
oc wait --timeout=10m --for=condition=ready pod -n nvidia-gpu-operator -l app=nvidia-device-plugin-daemonset
oc wait --timeout=10m --for=condition=ready pod -n nvidia-gpu-operator -l app=nvidia-container-toolkit-daemonset
oc wait --timeout=10m --for=condition=ready pod -n nvidia-gpu-operator -l app=nvidia-dcgm-exporter
oc wait --timeout=10m --for=condition=ready pod -n nvidia-gpu-operator -l app=gpu-feature-discovery

# Verify GPU nodes are labeled (this confirms GPUs are detected)
oc get nodes -l nvidia.com/gpu.present=true

# Check GPU resources are available on nodes
oc describe node <gpu-node-name> | grep nvidia.com/gpu

# Monitor ClusterPolicy status (state should become "ready")
oc get clusterpolicy gpu-cluster-policy -o jsonpath='{.status.state}'
```

The installation is complete when:
- All GPU operator daemonsets have pods in `Running` state
- GPU nodes have the `nvidia.com/gpu.present=true` label
- Nodes show available GPU resources (e.g., `nvidia.com/gpu: 8`)
- ClusterPolicy state is `ready`

## Deployment Guides

Once your cluster is configured with SR-IOV and GPU operators, you can deploy distributed inference workloads:

### Distributed Inference with llm-d

For detailed instructions on deploying OpenShift AI using the Distributed Inference Server:

- [Deploying a model by using the Distributed Inference Server with llm-d [Developer preview]](https://access.redhat.com/articles/7131048)

### Platform Setup

For complete platform setup including cert-manager, Service Mesh, and Leader Worker Set operators:

- [OpenShift 4.18 Setup Guide](./ocp-4-18-setup/README.md)

## Sample Configurations

This directory contains several example configurations:

### Single Node GPU Deployments

- [Single Node GPU Example](./single-node-gpu/) - Basic single-node inference setup

### Distributed Inference Examples

- [Data Parallel + Expert Parallel (DP+EP)](./dp-ep/deepseek-r1-gpu-rdma-roce/) - Advanced distributed inference with
  RDMA networking
- [Precise Prefix KV Cache Routing](./precise-prefix-kv-cache-routing/) - Optimized routing for precise KV cache re-use 

### Example: DeepSeek-R1 with GPU RDMA

See the [DeepSeek-R1 DP+EP examples](./dp-ep/deepseek-r1-gpu-rdma-roce/) for complete configurations including:

- SR-IOV network setup with RoCE
- LLM Inference Service with disaggregated Prefill/Decode serving and Data+Expert Parallelism
- Router and worker configurations

## Troubleshooting

### SR-IOV Issues

- Verify your nodes have RDMA-capable NICs
- Check node feature labels: `oc get nodes --show-labels`
- Review SR-IOV operator logs: `oc logs -n openshift-sriov-network-operator <pod-name>`

### GPU Issues

- Verify GPUs are detected: `oc describe node <node-name> | grep nvidia.com/gpu`
- Check ClusterPolicy status: `oc get clusterpolicy -o yaml`
- Review GPU operator logs: `oc logs -n nvidia-gpu-operator <pod-name>`

### RDMA Verification

- Check if RDMA devices are available on nodes: `oc debug node/<node-name> -- chroot /host rdma link`
- Verify RDMA resources: `oc describe node <node-name> | grep rdma`

## Additional Resources

- [KServe Documentation](https://kserve.github.io/website/)
- [NVIDIA GPU Operator Documentation](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/)
- [OpenShift SR-IOV Documentation](https://docs.redhat.com/en/documentation/openshift_container_platform/4.19/html-single/hardware_networks/index)
