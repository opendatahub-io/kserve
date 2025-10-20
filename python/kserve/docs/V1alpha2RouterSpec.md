# V1alpha2RouterSpec

RouterSpec defines the routing configuration for exposing the service. It supports Kubernetes Ingress and the Gateway API. The fields are mutually exclusive.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**gateway** | [**V1alpha2GatewaySpec**](V1alpha2GatewaySpec.md) |  | [optional] 
**ingress** | [**V1alpha2IngressSpec**](V1alpha2IngressSpec.md) |  | [optional] 
**route** | [**V1alpha2GatewayRoutesSpec**](V1alpha2GatewayRoutesSpec.md) |  | [optional] 
**scheduler** | [**V1alpha2SchedulerSpec**](V1alpha2SchedulerSpec.md) |  | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


