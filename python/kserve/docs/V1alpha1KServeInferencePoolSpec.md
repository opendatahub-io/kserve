# V1alpha1KServeInferencePoolSpec

KServeInferencePoolSpec uses plain types for cross-version compatibility. Converted to GIE v1/v1alpha2 types at runtime by the controller.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**endpoint_picker_ref** | [**V1alpha1KServeEndpointPickerRef**](V1alpha1KServeEndpointPickerRef.md) |  | 
**selector** | [**V1alpha1KServeLabelSelector**](V1alpha1KServeLabelSelector.md) |  | 
**target_ports** | [**list[V1alpha1KServePort]**](V1alpha1KServePort.md) | TargetPorts defines ports exposed by this pool (max 1 port). | 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


