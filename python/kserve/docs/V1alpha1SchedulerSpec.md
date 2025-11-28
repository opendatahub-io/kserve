# V1alpha1SchedulerSpec

SchedulerSpec defines the Inference Gateway extension configuration.  The SchedulerSpec configures the connection from the Gateway to the model deployment leveraging the LLM optimized request Scheduler, also known as the Endpoint Picker (EPP) which determines the exact pod that should handle the request and responds back to Envoy with the target pod, Envoy will then forward the request to the chosen pod.  The Scheduler is only effective when having multiple inference pod replicas.  Step 1: Gateway (Envoy) <-- ExtProc --> EPP (select the optimal replica to handle the request) Step 2: Gateway (Envoy) <-- forward request --> Inference Pod X
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**pool** | [**V1alpha1InferencePoolSpec**](V1alpha1InferencePoolSpec.md) |  | [optional] 
**template** | [**V1PodSpec**](V1PodSpec.md) |  | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


