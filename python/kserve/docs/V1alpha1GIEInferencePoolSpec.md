# V1alpha1GIEInferencePoolSpec

GIEInferencePoolSpec defines the desired state of InferencePool
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**extension_ref** | [**V1alpha1Extension**](V1alpha1Extension.md) |  | [optional] 
**selector** | **dict(str, str)** | Selector defines a map of labels to watch model server pods that should be included in the InferencePool. In some cases, implementations may translate this field to a Service selector, so this matches the simple map used for Service selectors instead of the full Kubernetes LabelSelector type. If sepecified, it will be applied to match the model server pods in the same namespace as the InferencePool. Cross namesoace selector is not supported. | 
**target_port_number** | **int** | TargetPortNumber defines the port number to access the selected model servers. The number must be in the range 1 to 65535. | [default to 0]

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


