# V1alpha2ParallelismSpec

ParallelismSpec defines the parallelism parameters for distributed inference.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**data** | **int** | Data parallelism size. | [optional] 
**data_local** | **int** | DataLocal data local parallelism size. | [optional] 
**data_rpc_port** | **int** | DataRPCPort is the data parallelism RPC port. | [optional] 
**expert** | **bool** | Expert enables expert parallelism. | [optional] 
**pipeline** | **int** | Pipeline parallelism size. | [optional] 
**tensor** | **int** | Tensor parallelism size. | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


