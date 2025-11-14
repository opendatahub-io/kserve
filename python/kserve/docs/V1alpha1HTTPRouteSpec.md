# V1alpha1HTTPRouteSpec

HTTPRouteSpec defines configurations for a Gateway API HTTPRoute. 'Spec' and 'Refs' are mutually exclusive and determine whether the route is managed by the controller or user-managed.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**refs** | [**list[V1LocalObjectReference]**](https://github.com/kubernetes-client/python/blob/master/kubernetes/docs/V1LocalObjectReference.md) | Refs provides references to existing, user-managed HTTPRoute objects (\&quot;Bring Your Own\&quot; route). The controller will validate the existence of these routes but will not modify them. | [optional] 
**spec** | [**SigsK8sIoGatewayApiApisV1HTTPRouteSpec**](SigsK8sIoGatewayApiApisV1HTTPRouteSpec.md) |  | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


