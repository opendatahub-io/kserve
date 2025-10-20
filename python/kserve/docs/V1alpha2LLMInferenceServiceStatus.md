# V1alpha2LLMInferenceServiceStatus

LLMInferenceServiceStatus defines the observed state of LLMInferenceService.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**address** | [**KnativeAddressable**](KnativeAddressable.md) |  | [optional] 
**addresses** | [**list[KnativeAddressable]**](KnativeAddressable.md) | Addresses is a list of addresses for different protocols (HTTP and HTTPS) If Addresses is present, Address must be ignored by clients. | [optional] 
**annotations** | **dict(str, str)** | Annotations is additional Status fields for the Resource to save some additional State as well as convey more information to the user. This is roughly akin to Annotations on any k8s resource, just the reconciler conveying richer information outwards. | [optional] 
**conditions** | [**list[KnativeCondition]**](KnativeCondition.md) | Conditions the latest available observations of a resource&#39;s current state. | [optional] 
**observed_generation** | **int** | ObservedGeneration is the &#39;Generation&#39; of the Service that was last processed by the controller. | [optional] 
**url** | [**KnativeURL**](KnativeURL.md) |  | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


