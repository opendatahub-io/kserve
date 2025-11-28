# V1alpha1LLMInferenceServiceSpec

LLMInferenceServiceSpec defines the desired state of LLMInferenceService.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**base_refs** | [**list[V1LocalObjectReference]**](https://github.com/kubernetes-client/python/blob/master/kubernetes/docs/V1LocalObjectReference.md) | BaseRefs allows inheriting and overriding configurations from one or more LLMInferenceServiceConfig instances. The controller merges these base configurations, with the current LLMInferenceService spec taking the highest precedence. When multiple baseRefs are provided, the last one in the list overrides previous ones. | [optional] 
**model** | [**V1alpha1LLMModelSpec**](V1alpha1LLMModelSpec.md) |  | [optional] 
**parallelism** | [**V1alpha1ParallelismSpec**](V1alpha1ParallelismSpec.md) |  | [optional] 
**prefill** | [**V1alpha1WorkloadSpec**](V1alpha1WorkloadSpec.md) |  | [optional] 
**replicas** | **int** | Number of replicas for the deployment. | [optional] 
**router** | [**V1alpha1RouterSpec**](V1alpha1RouterSpec.md) |  | [optional] 
**template** | [**V1PodSpec**](V1PodSpec.md) |  | [optional] 
**worker** | [**V1PodSpec**](V1PodSpec.md) |  | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


