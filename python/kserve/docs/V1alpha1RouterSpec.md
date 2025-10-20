# V1alpha1RouterSpec

RouterSpec defines the routing configuration for exposing the service. It supports Kubernetes Ingress and the Gateway API. The fields are mutually exclusive.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**gateway** | [**V1alpha1GatewaySpec**](V1alpha1GatewaySpec.md) |  | [optional] 
**ingress** | [**V1alpha1IngressSpec**](V1alpha1IngressSpec.md) |  | [optional] 
**route** | [**V1alpha1GatewayRoutesSpec**](V1alpha1GatewayRoutesSpec.md) |  | [optional] 
**scheduler** | [**V1alpha1SchedulerSpec**](V1alpha1SchedulerSpec.md) |  | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


