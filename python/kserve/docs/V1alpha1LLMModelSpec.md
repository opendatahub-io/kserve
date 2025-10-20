# V1alpha1LLMModelSpec

LLMModelSpec defines the model source and its characteristics.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**criticality** | **str** | Criticality defines how important it is to serve the model compared to other models. This is used by the Inference Gateway scheduler. | [optional] 
**lora** | [**V1alpha1LoRASpec**](V1alpha1LoRASpec.md) |  | [optional] 
**name** | **str** | Name is the name of the model as it will be set in the \&quot;model\&quot; parameter for an incoming request. If omitted, it will default to &#x60;metadata.name&#x60;. For LoRA adapters, this field is required. | [optional] 
**uri** | [**KnativeURL**](KnativeURL.md) |  | 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


