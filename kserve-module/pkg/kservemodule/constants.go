package kservemodule

import "k8s.io/apimachinery/pkg/runtime/schema"

var unownedGroupKinds = map[schema.GroupKind]struct{}{
	{Group: llmISVCConfigGroup, Kind: llmISVCConfigKind}: {},
}

const (
	// Component names
	KserveComponentName             = "kserve"
	OdhModelControllerComponentName = "modelcontroller"
	WVAComponentName                = "wva"
	ModelCacheComponentName         = "modelcache"
	ObservabilityComponentName      = "observability"
	ConsoleDashboardsComponentName  = "console-dashboards"

	// Manifest source paths
	KserveManifestSourcePath        = "overlays/odh"
	KserveManifestSourcePathXKS     = "overlays/odh-xks"
	ModelCacheManifestSourcePath    = "overlays/odh-modelcache"
	ModelControllerSourcePath       = "base"
	WVAManifestSourcePathOCP        = "overlays/namespace-scoped/openshift"
	ObservabilityManifestSourcePath      = "monitoring/llmisvc/dashboards"
	ConsoleDashboardsManifestSourcePath = "monitoring/llmisvc/dashboards-odc"

	// Deployment names
	kserveControllerDeployment     = "kserve-controller-manager"
	llmISVCControllerDeployment    = "llmisvc-controller-manager"
	localmodelControllerDeployment = "kserve-localmodel-controller-manager"
	odhModelControllerDeployment   = "odh-model-controller"
	wvaControllerDeployment        = "workload-variant-autoscaler-controller-manager"

	// Console dashboards target namespace
	consoleDashboardsNamespace = "openshift-config-managed"

	// SSA field manager
	fieldOwner = "kserve-module-controller"

	// Platform version ConfigMap
	platformVersionConfigMap    = "odh-kserve-config"
	platformVersionConfigMapKey = "platformVersion"

	// ConfigMap keys
	kserveConfigMapName     = "inferenceservice-config"
	ingressConfigKeyName    = "ingress"
	serviceConfigKeyName    = "service"
	configHashAnnotationKey = "kserve-module/config-hash"
	oauthProxyConfigKeyName = "oauthProxy"
	openshiftConfigKeyName  = "openshiftConfig"

	// LLMInferenceServiceConfig versioning
	wellKnownAnnotationKey   = "serving.kserve.io/well-known-config"
	wellKnownAnnotationValue = "true"
	llmISVCConfigPrefixEnv   = "LLM_INFERENCE_SERVICE_CONFIG_PREFIX"
	llmISVCConfigGroup       = "serving.kserve.io"
	llmISVCConfigKind        = "LLMInferenceServiceConfig"
	llmISVCKind              = "LLMInferenceService"
	llmISVCConfigFinalizer   = "serving.kserve.io/llmisvcconfig-finalizer"

	// Template (ServingRuntime) resource type
	templateGroup = "template.openshift.io"
	templateKind  = "Template"

	// PlatformFinalizerName is set on the Kserve CR by the platform operator;
	// the module operator removes it after cleanup is complete.
	PlatformFinalizerName = "platform.opendatahub.io/finalizer"

	// cert-manager defaults
	defaultCAIssuerName    = "opendatahub-ca-issuer"
	defaultIssuerRefKind   = "ClusterIssuer"
	defaultCertName        = "opendatahub-ca"
	defaultCertManagerNS   = "cert-manager"
	defaultIstioCACertPath = "/var/run/secrets/opendatahub/ca.crt"
)
