# V1alpha1Extension

Extension specifies how to configure an extension that runs the endpoint picker.
## Properties
Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**failure_mode** | **str** | Configures how the gateway handles the case when the extension is not responsive. Defaults to failClose. | [optional] 
**group** | **str** | Group is the group of the referent. The default value is \&quot;\&quot;, representing the Core API group. | [optional] 
**kind** | **str** | Kind is the Kubernetes resource kind of the referent. For example \&quot;Service\&quot;.  Defaults to \&quot;Service\&quot; when not specified.  ExternalName services can refer to CNAME DNS records that may live outside of the cluster and as such are difficult to reason about in terms of conformance. They also may not be safe to forward to (see CVE-2021-25740 for more information). Implementations MUST NOT support ExternalName Services. | [optional] 
**name** | **str** | Name is the name of the referent. | [optional] [default to '']
**port_number** | **int** | The port number on the service running the extension. When unspecified, implementations SHOULD infer a default value of 9002 when the Kind is Service. | [optional] 

[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


