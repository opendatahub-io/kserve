package kservemodule

import "os"

var kserveImageParamMap = map[string]string{
	"kserve-controller":                "RELATED_IMAGE_ODH_KSERVE_CONTROLLER_IMAGE",
	"llmisvc-controller":               "RELATED_IMAGE_ODH_KSERVE_LLMISVC_CONTROLLER_IMAGE",
	"kserve-agent":                     "RELATED_IMAGE_ODH_KSERVE_AGENT_IMAGE",
	"kserve-router":                    "RELATED_IMAGE_ODH_KSERVE_ROUTER_IMAGE",
	"kserve-storage-initializer":       "RELATED_IMAGE_ODH_KSERVE_STORAGE_INITIALIZER_IMAGE",
	"kserve-llm-d":                     "RELATED_IMAGE_RHAII_VLLM_CUDA_IMAGE",
	"kserve-llm-d-nvidia-cuda":         "RELATED_IMAGE_RHAII_VLLM_CUDA_IMAGE",
	"kserve-llm-d-nvidia-cuda-fast-1":  "RELATED_IMAGE_RHAII_VLLM_CUDA_FAST_1_IMAGE",
	"kserve-llm-d-nvidia-cuda-fast-2":  "RELATED_IMAGE_RHAII_VLLM_CUDA_FAST_2_IMAGE",
	"kserve-llm-d-amd-rocm":            "RELATED_IMAGE_RHAII_VLLM_ROCM_IMAGE",
	"kserve-llm-d-amd-rocm-fast-1":     "RELATED_IMAGE_RHAII_VLLM_ROCM_FAST_1_IMAGE",
	"kserve-llm-d-amd-rocm-fast-2":     "RELATED_IMAGE_RHAII_VLLM_ROCM_FAST_2_IMAGE",
	"kserve-llm-d-intel-gaudi":         "RELATED_IMAGE_RHAII_VLLM_GAUDI_IMAGE",
	"kserve-llm-d-intel-gaudi-fast-1":  "RELATED_IMAGE_RHAII_VLLM_GAUDI_FAST_1_IMAGE",
	"kserve-llm-d-intel-gaudi-fast-2":  "RELATED_IMAGE_RHAII_VLLM_GAUDI_FAST_2_IMAGE",
	"kserve-llm-d-ibm-spyre":           "RELATED_IMAGE_RHAII_VLLM_SPYRE_IMAGE",
	"kserve-llm-d-ibm-spyre-fast-1":    "RELATED_IMAGE_RHAII_VLLM_SPYRE_FAST_1_IMAGE",
	"kserve-llm-d-ibm-spyre-fast-2":    "RELATED_IMAGE_RHAII_VLLM_SPYRE_FAST_2_IMAGE",
	"kserve-llm-d-inference-scheduler": "RELATED_IMAGE_ODH_LLM_D_INFERENCE_SCHEDULER_IMAGE",
	"kserve-llm-d-routing-sidecar":     "RELATED_IMAGE_ODH_LLM_D_ROUTING_SIDECAR_IMAGE",
	"kserve-llm-d-uds-tokenizer":       "RELATED_IMAGE_ODH_LLM_D_KV_CACHE_IMAGE",
	"kube-rbac-proxy":                  "RELATED_IMAGE_ODH_KUBE_RBAC_PROXY_IMAGE",
	"kserve-localmodel-controller":     "RELATED_IMAGE_ODH_KSERVE_LOCALMODEL_CONTROLLER_IMAGE",
	"kserve-localmodelnode-agent":      "RELATED_IMAGE_ODH_KSERVE_LOCALMODELNODE_AGENT_IMAGE",
}

var modelControllerImageParamMap = map[string]string{
	"odh-model-controller":    "RELATED_IMAGE_ODH_MODEL_CONTROLLER_IMAGE",
	"odh-model-serving-api":   "RELATED_IMAGE_ODH_MODEL_SERVING_API_IMAGE",
	"caikit-standalone-image": "RELATED_IMAGE_ODH_CAIKIT_NLP_IMAGE",
	"ovms-image":              "RELATED_IMAGE_ODH_OPENVINO_MODEL_SERVER_IMAGE",
	"mlserver-image":          "RELATED_IMAGE_ODH_MLSERVER_IMAGE",
	"vllm-cuda-image":         "RELATED_IMAGE_RHAII_VLLM_CUDA_IMAGE",
	"vllm-cuda-image-fast-1":  "RELATED_IMAGE_RHAII_VLLM_CUDA_FAST_1_IMAGE",
	"vllm-cuda-image-fast-2":  "RELATED_IMAGE_RHAII_VLLM_CUDA_FAST_2_IMAGE",
	"vllm-rocm-image":         "RELATED_IMAGE_RHAII_VLLM_ROCM_IMAGE",
	"vllm-rocm-image-fast-1":  "RELATED_IMAGE_RHAII_VLLM_ROCM_FAST_1_IMAGE",
	"vllm-rocm-image-fast-2":  "RELATED_IMAGE_RHAII_VLLM_ROCM_FAST_2_IMAGE",
	"vllm-gaudi-image":        "RELATED_IMAGE_RHAII_VLLM_GAUDI_IMAGE",
	"vllm-gaudi-image-fast-1": "RELATED_IMAGE_RHAII_VLLM_GAUDI_FAST_1_IMAGE",
	"vllm-gaudi-image-fast-2": "RELATED_IMAGE_RHAII_VLLM_GAUDI_FAST_2_IMAGE",
	"vllm-spyre-image":        "RELATED_IMAGE_RHAII_VLLM_SPYRE_IMAGE",
	"vllm-spyre-image-fast-1": "RELATED_IMAGE_RHAII_VLLM_SPYRE_FAST_1_IMAGE",
	"vllm-spyre-image-fast-2": "RELATED_IMAGE_RHAII_VLLM_SPYRE_FAST_2_IMAGE",
	"vllm-cpu-image":              "RELATED_IMAGE_ODH_VLLM_CPU_IMAGE",
	"vllm-cpu-image-fast-1":       "RELATED_IMAGE_ODH_VLLM_CPU_FAST_1_IMAGE",
	"vllm-cpu-image-fast-2":       "RELATED_IMAGE_ODH_VLLM_CPU_FAST_2_IMAGE",
	"vllm-cpu-x86-image":          "RELATED_IMAGE_RHAII_VLLM_CPU_IMAGE",
	"vllm-cpu-x86-image-fast-1":   "RELATED_IMAGE_RHAII_VLLM_CPU_FAST_1_IMAGE",
	"vllm-cpu-x86-image-fast-2":   "RELATED_IMAGE_RHAII_VLLM_CPU_FAST_2_IMAGE",
	"guardrails-detector-huggingface-runtime-image": "RELATED_IMAGE_ODH_GUARDRAILS_DETECTOR_HUGGINGFACE_RUNTIME_IMAGE",
}

var wvaImageParamMap = map[string]string{
	"wva-controller-image": "RELATED_IMAGE_ODH_WORKLOAD_VARIANT_AUTOSCALER_CONTROLLER_IMAGE",
}


func buildCertManagerParams(namespace string) map[string]string {
	return map[string]string{
		"NAMESPACE":                 namespace,
		"ISSUER_REF_NAME":           getEnvOrDefault("ISSUER_NAME", defaultCAIssuerName),
		"ISSUER_REF_KIND":           getEnvOrDefault("ISSUER_KIND", defaultIssuerRefKind),
		"ISSUER_REF_GROUP":          "cert-manager.io",
		"CA_SECRET_NAME":            getEnvOrDefault("CA_SECRET_NAME", defaultCertName),
		"CA_SECRET_NAMESPACE":       getEnvOrDefault("CA_SECRET_NAMESPACE", defaultCertManagerNS),
		"ISTIO_CA_CERTIFICATE_PATH": getEnvOrDefault("ISTIO_CA_CERT_PATH", defaultIstioCACertPath),
	}
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
