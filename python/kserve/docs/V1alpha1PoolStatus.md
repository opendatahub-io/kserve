# V1alpha1PoolStatus

PoolStatus defines the observed state of InferencePool from a Gateway.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**conditions** | [**list[V1Condition]**](V1Condition.md) | Conditions track the state of the InferencePool.  Known condition types are:  * \&quot;Accepted\&quot; * \&quot;ResolvedRefs\&quot; | [optional] 
**parent_ref** | [**V1alpha1ParentGatewayReference**](V1alpha1ParentGatewayReference.md) |  | 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


