package kservemodule

const (
	// Component names
	KserveComponentName             = "kserve"
	OdhModelControllerComponentName = "modelcontroller"
	WVAComponentName                = "wva"
	ModelCacheComponentName         = "modelcache"
	ObservabilityComponentName      = "observability"

	// Manifest source paths
	KserveManifestSourcePath        = "overlays/odh"
	KserveManifestSourcePathXKS     = "overlays/odh-xks"
	ModelCacheManifestSourcePath    = "overlays/odh-modelcache"
	ModelControllerSourcePath       = "base"
	WVAManifestSourcePathOCP        = "overlays/namespace-scoped/openshift"
	ObservabilityManifestSourcePath = "monitoring/llmisvc/dashboards"

	// Deployment names
	kserveControllerDeployment     = "kserve-controller-manager"
	llmISVCControllerDeployment    = "llmisvc-controller-manager"
	localmodelControllerDeployment = "kserve-localmodel-controller-manager"
	odhModelControllerDeployment   = "odh-model-controller"
	wvaControllerDeployment        = "workload-variant-autoscaler-controller-manager"

	// SSA field manager
	fieldOwner = "kserve-module-controller"

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

	// Template (ServingRuntime) resource type
	templateGroup = "template.openshift.io"
	templateKind  = "Template"

	// cert-manager defaults
	defaultCAIssuerName    = "opendatahub-ca-issuer"
	defaultIssuerRefKind   = "ClusterIssuer"
	defaultCertName        = "opendatahub-ca"
	defaultCertManagerNS   = "cert-manager"
	defaultIstioCACertPath = "/var/run/secrets/opendatahub/ca.crt"
)
