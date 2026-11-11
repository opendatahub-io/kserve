/*
Copyright 2021 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package constants

// InferenceGraph midstream constants
const (
	InferenceGraphAuthCRBName   = "kserve-inferencegraph-auth-verifiers"
	InferenceGraphFinalizerName = "inferencegraph.finalizers"
)

// Midstream annotation keys
const (
	// EnableTracingEgressNetworkPolicyAnnotationKey opts an LLMInferenceService into the workload tracing egress policy.
	EnableTracingEgressNetworkPolicyAnnotationKey = KServeAPIGroupName + "/enable-tracing-egress-network-policy"
	// LLMTracingServiceStatusAnnotationKey records the resolved OTLP Service key
	// in LLMInferenceService status so Service events can be indexed precisely.
	LLMTracingServiceStatusAnnotationKey = KServeAPIGroupName + "/tracing-egress-service"
)

var (
	OVMSAutoVersioningAnnotationKey = "storage.kserve.io/ovms-auto-versioning"
	ODHKserveRuntimeAnnotation      = "opendatahub.io/kserve-runtime"
)

// Midstream opendatahub.io/kserve-runtime annotation values
const (
	ODHKserveRuntimeVLLM = "vllm"
)

// AuditLoggingProfile is the resolved profile passed to raw workload reconcilers.
type AuditLoggingProfile string

// Midstream networking constants
const (
	ODHKserveRawAuth                                           = "security.opendatahub.io/enable-auth"
	ODHKserveAuditLoggingProfile                               = "observability.opendatahub.io/audit-logging-profile"
	AuditLoggingProfileNone                AuditLoggingProfile = "none"
	AuditLoggingProfileMetadata            AuditLoggingProfile = "metadata"
	ODHAuthProxyTypeAnnotation                                 = "security.opendatahub.io/auth-proxy-type"
	ODHKserveAuthProxyTLSManagedAnnotation                     = "security.opendatahub.io/auth-proxy-tls-managed"
	ODHRouteEnabled                                            = "exposed"
	ServingCertSecretSuffix                                    = "-serving-cert"
	OpenshiftServingCertAnnotation                             = "service.beta.openshift.io/serving-cert-secret-name"
)

// Midstream container names
const (
	OVMSVersioningContainerName = "ovms-auto-versioning"
)

// Midstream auth proxy constants
const (
	OauthProxyPort                  = 8443
	OauthProxyProbePort             = 8643
	OauthProxyResourceMemoryLimit   = "128Mi"
	OauthProxyResourceCPULimit      = "200m"
	OauthProxyResourceMemoryRequest = "64Mi"
	OauthProxyResourceCPURequest    = "100m"
	OauthProxySARCMName             = "kube-rbac-proxy-sar-config"
	// Used for test purposes
	OauthProxyImage         = "quay.io/opendatahub/odh-kube-auth-proxy@sha256:dcb09fbabd8811f0956ef612a0c9ddd5236804b9bd6548a0647d2b531c9d01b3"
	DefaultServiceAccount   = "default"
	KubeRbacContainerName   = "kube-rbac-proxy"
	OauthProxyContainerName = "oauth-proxy"
	KubeRbacProxyType       = "kube-rbac-proxy"
)

// OpenShift constants
const (
	OpenShiftServiceCaConfigMapName = "openshift-service-ca.crt"
)

// ODHKServeCustomCABundleConfigMapName is the CA bundle ConfigMap odh-model-controller keeps in user namespaces.
const ODHKServeCustomCABundleConfigMapName = "odh-kserve-custom-ca-bundle"

// ODH Connections API
const ODHS3Endpoint = "AWS_S3_ENDPOINT"

// ModelCache download Job pod labels selecting the pods covered by the ODH egress
// NetworkPolicies: the static `kserve-localmodel-download` policy shipped in the
// odh-modelcache overlay (config/overlays/odh-modelcache/network-policies.yaml) for
// per-node downloads, and the per-cache `<cache-name>-import-egress` policy for
// shared-PVC imports (localmodel/reconcilers/sharedpvc_platform_odh.go). The overlay
// manifest owns the static policy's lifecycle: no controller creates, watches, or
// cleans it, so the label values here and the policy selectors must stay in sync with
// that manifest.
const (
	// DownloadJobLabelKey is the label key stamped on download Job pod templates
	// so the ODH egress NetworkPolicies can select them.
	DownloadJobLabelKey = "serving.kserve.io/localmodel-download"
	// DownloadJobLabelValueNode marks per-node download Jobs launched by the
	// localmodelnode agent; only these pods match the overlay policy.
	DownloadJobLabelValueNode = "node"
	// DownloadJobLabelValueSharedPVC marks shared-PVC import Jobs created by the
	// localmodel manager.
	DownloadJobLabelValueSharedPVC = "shared-pvc"
)

// TLS infrastructure for service-ca bundle mounting and transformer-to-predictor communication (OpenShift-specific)
const (
	ServiceCaBundleVolumeName = "openshift-service-ca-bundle"
	ServiceCaBundleMountPath  = "/etc/odh/openshift-service-ca-bundle"
	ServiceCaBundleCertFile   = "service-ca.crt"

	// Transformer serving-cert volume/mount for native TLS (HTTPS on 8443)
	TransformerTLSVolumeName = "transformer-tls"
	TransformerTLSMountPath  = "/etc/tls/private"
	TransformerHTTPSPort     = int32(8443)
)

func init() {
	ServiceAnnotationDisallowedList = append(ServiceAnnotationDisallowedList, ODHKserveRawAuth, ODHKserveAuditLoggingProfile)
}
