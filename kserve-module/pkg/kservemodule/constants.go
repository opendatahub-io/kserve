package kservemodule

const (
	// Component names
	kserveComponentName             = "kserve"
	odhModelControllerComponentName = "odh-model-controller"

	// Manifest source paths
	kserveManifestSourcePath    = "overlays/odh"
	kserveManifestSourcePathXKS = "overlays/odh-xks"
	modelControllerSourcePath   = "base"

	// Deployment names
	kserveControllerDeployment  = "kserve-controller-manager"
	llmISVCControllerDeployment = "llmisvc-controller-manager"
	//TO-DO
	// localmodelControllerDeployment = "kserve-localmodel-controller-manager"
	odhModelControllerDeployment = "odh-model-controller"

	// SSA field manager
	fieldOwner = "kserve-module-controller"

	// ConfigMap keys
	kserveConfigMapName     = "inferenceservice-config"
	ingressConfigKeyName    = "ingress"
	serviceConfigKeyName    = "service"
	configHashAnnotationKey = "kserve-module/config-hash"

	// LLMInferenceServiceConfig versioning
	wellKnownAnnotationKey   = "serving.kserve.io/well-known-config"
	wellKnownAnnotationValue = "true"
	llmISVCConfigPrefix      = "LLM_INFERENCE_SERVICE_CONFIG_PREFIX"
	llmISVCConfigGroup       = "serving.kserve.io"
	llmISVCConfigKind        = "LLMInferenceServiceConfig"

	// cert-manager defaults
	defaultCAIssuerName    = "opendatahub-ca-issuer"
	defaultIssuerRefKind   = "ClusterIssuer"
	defaultCertName        = "opendatahub-ca"
	defaultCertManagerNS   = "cert-manager"
	defaultIstioCACertPath = "/var/run/secrets/opendatahub/ca.crt"
)
