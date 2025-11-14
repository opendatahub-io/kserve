# V1alpha1InferencePoolStatus

InferencePoolStatus defines the observed state of InferencePool.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**parent** | [**list[V1alpha1PoolStatus]**](V1alpha1PoolStatus.md) | Parents is a list of parent resources (usually Gateways) that are associated with the InferencePool, and the status of the InferencePool with respect to each parent.  A maximum of 32 Gateways will be represented in this list. When the list contains &#x60;kind: Status, name: default&#x60;, it indicates that the InferencePool is not associated with any Gateway and a controller must perform the following:   - Remove the parent when setting the \&quot;Accepted\&quot; condition.  - Add the parent when the controller will no longer manage the InferencePool    and no other parents exist. | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


