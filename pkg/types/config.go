package types

type StorageInitializerConfig struct {
	Image                      string `json:"image"`
	CpuRequest                 string `json:"cpuRequest"`
	CpuLimit                   string `json:"cpuLimit"`
	CpuModelcar                string `json:"cpuModelcar"`
	MemoryRequest              string `json:"memoryRequest"`
	MemoryLimit                string `json:"memoryLimit"`
	CaBundleConfigMapName      string `json:"caBundleConfigMapName"`
	CaBundleVolumeMountPath    string `json:"caBundleVolumeMountPath"`
	MemoryModelcar             string `json:"memoryModelcar"`
	EnableDirectPvcVolumeMount bool   `json:"enableDirectPvcVolumeMount"`
	EnableOciImageSource       bool   `json:"enableModelcar"`
	UidModelcar                *int64 `json:"uidModelcar"`
}
