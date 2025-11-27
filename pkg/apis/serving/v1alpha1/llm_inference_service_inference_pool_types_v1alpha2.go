/*
Copyright 2023 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Copied from https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/release-0.5/api/v1alpha2/inferencepool_types.go

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	igwv1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
)

// Criticality defines how important it is to serve the model compared to other models.
// Criticality is intentionally a bounded enum to contain the possibilities that need to be supported by the load balancing algorithm. Any reference to the Criticality field must be optional (use a pointer), and set no default.
// This allows us to union this with a oneOf field in the future should we wish to adjust/extend this behavior.
// +kubebuilder:validation:Enum=Critical;Standard;Sheddable
type Criticality string

// GIEInferencePoolSpec defines the desired state of InferencePool
type GIEInferencePoolSpec struct {
	// Selector defines a map of labels to watch model server pods
	// that should be included in the InferencePool.
	// In some cases, implementations may translate this field to a Service selector, so this matches the simple
	// map used for Service selectors instead of the full Kubernetes LabelSelector type.
	// If sepecified, it will be applied to match the model server pods in the same namespace as the InferencePool.
	// Cross namesoace selector is not supported.
	//
	// +kubebuilder:validation:Required
	Selector map[igwv1.LabelKey]igwv1.LabelValue `json:"selector"`

	// TargetPortNumber defines the port number to access the selected model servers.
	// The number must be in the range 1 to 65535.
	//
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:validation:Required
	TargetPortNumber int32 `json:"targetPortNumber"`

	// EndpointPickerConfig specifies the configuration needed by the proxy to discover and connect to the endpoint
	// picker service that picks endpoints for the requests routed to this pool.
	EndpointPickerConfig `json:",inline"`
}

// EndpointPickerConfig specifies the configuration needed by the proxy to discover and connect to the endpoint picker extension.
// This type is intended to be a union of mutually exclusive configuration options that we may add in the future.
type EndpointPickerConfig struct {
	// Extension configures an endpoint picker as an extension service.
	//
	// +kubebuilder:validation:Required
	ExtensionRef *Extension `json:"extensionRef,omitempty"`
}

// Extension specifies how to configure an extension that runs the endpoint picker.
type Extension struct {
	// Reference is a reference to a service extension. When ExtensionReference is invalid,
	// a 5XX status code MUST be returned for the request that would have otherwise been routed
	// to the invalid backend.
	ExtensionReference `json:",inline"`

	// ExtensionConnection configures the connection between the gateway and the extension.
	ExtensionConnection `json:",inline"`
}

// ExtensionReference is a reference to the extension.
//
// Connections to this extension MUST use TLS by default. Implementations MAY
// provide a way to customize this connection to use cleartext, a different
// protocol, or custom TLS configuration.
//
// If a reference is invalid, the implementation MUST update the `ResolvedRefs`
// Condition on the InferencePool's status to `status: False`. A 5XX status code
// MUST be returned for the request that would have otherwise been routed to the
// invalid backend.
type ExtensionReference struct {
	// Group is the group of the referent.
	// The default value is "", representing the Core API group.
	//
	// +optional
	// +kubebuilder:default=""
	Group *igwv1.Group `json:"group,omitempty"`

	// Kind is the Kubernetes resource kind of the referent. For example
	// "Service".
	//
	// Defaults to "Service" when not specified.
	//
	// ExternalName services can refer to CNAME DNS records that may live
	// outside of the cluster and as such are difficult to reason about in
	// terms of conformance. They also may not be safe to forward to (see
	// CVE-2021-25740 for more information). Implementations MUST NOT
	// support ExternalName Services.
	//
	// +optional
	// +kubebuilder:default=Service
	Kind *igwv1.Kind `json:"kind,omitempty"`

	// Name is the name of the referent.
	//
	// +kubebuilder:validation:Required
	Name igwv1.ObjectName `json:"name"`

	// The port number on the service running the extension. When unspecified,
	// implementations SHOULD infer a default value of 9002 when the Kind is
	// Service.
	//
	// +optional
	PortNumber *igwv1.PortNumber `json:"portNumber,omitempty"`
}

// ExtensionConnection encapsulates options that configures the connection to the extension.
type ExtensionConnection struct {
	// Configures how the gateway handles the case when the extension is not responsive.
	// Defaults to failClose.
	//
	// +optional
	// +kubebuilder:default="FailClose"
	FailureMode *ExtensionFailureMode `json:"failureMode"`
}

// ExtensionFailureMode defines the options for how the gateway handles the case when the extension is not
// responsive.
// +kubebuilder:validation:Enum=FailOpen;FailClose
type ExtensionFailureMode string

const (
	// FailOpen specifies that the proxy should not drop the request and forward the request to and endpoint of its picking.
	FailOpen ExtensionFailureMode = "FailOpen"
	// FailClose specifies that the proxy should drop the request.
	FailClose ExtensionFailureMode = "FailClose"
)

// InferencePoolStatus defines the observed state of InferencePool.
type InferencePoolStatus struct {
	// Parents is a list of parent resources (usually Gateways) that are
	// associated with the InferencePool, and the status of the InferencePool with respect to
	// each parent.
	//
	// A maximum of 32 Gateways will be represented in this list. When the list contains
	// `kind: Status, name: default`, it indicates that the InferencePool is not
	// associated with any Gateway and a controller must perform the following:
	//
	//  - Remove the parent when setting the "Accepted" condition.
	//  - Add the parent when the controller will no longer manage the InferencePool
	//    and no other parents exist.
	//
	// +kubebuilder:validation:MaxItems=32
	Parents []PoolStatus `json:"parent,omitempty"`
}

// PoolStatus defines the observed state of InferencePool from a Gateway.
type PoolStatus struct {
	// GatewayRef indicates the gateway that observed state of InferencePool.
	GatewayRef ParentGatewayReference `json:"parentRef"`

	// Conditions track the state of the InferencePool.
	//
	// Known condition types are:
	//
	// * "Accepted"
	// * "ResolvedRefs"
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:default={{type: "Accepted", status: "Unknown", reason:"Pending", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"}}
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// InferencePoolConditionType is a type of condition for the InferencePool
type InferencePoolConditionType string

// InferencePoolReason is the reason for a given InferencePoolConditionType
type InferencePoolReason string

// ParentGatewayReference identifies an API object including its namespace,
// defaulting to Gateway.
type ParentGatewayReference struct {
	// Group is the group of the referent.
	//
	// +optional
	// +kubebuilder:default="gateway.networking.k8s.io"
	Group *igwv1.Group `json:"group"`

	// Kind is kind of the referent. For example "Gateway".
	//
	// +optional
	// +kubebuilder:default=Gateway
	Kind *igwv1.Kind `json:"kind"`

	// Name is the name of the referent.
	Name igwv1.ObjectName `json:"name"`

	// Namespace is the namespace of the referent.  If not present,
	// the namespace of the referent is assumed to be the same as
	// the namespace of the referring object.
	//
	// +optional
	Namespace *igwv1.Namespace `json:"namespace,omitempty"`
}
