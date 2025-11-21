# V1alpha2WorkloadSpec

WorkloadSpec defines the configuration for a deployment workload, such as replicas and pod specifications.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**parallelism** | [**V1alpha2ParallelismSpec**](V1alpha2ParallelismSpec.md) |  | [optional] 
**replicas** | **int** | Number of replicas for the deployment. | [optional] 
**template** | [**V1PodSpec**](V1PodSpec.md) |  | [optional] 
**worker** | [**V1PodSpec**](V1PodSpec.md) |  | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


