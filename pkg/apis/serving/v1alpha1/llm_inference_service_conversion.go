/*
Copyright 2025 The KServe Authors.

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

package v1alpha1

import (
	"encoding/json"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/conversion"
	igwv1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

const (
	// CriticalityAnnotation is used to store v1alpha1 Criticality field during conversion
	CriticalityAnnotation = "serving.kserve.io/criticality"
)

// Ensure v1alpha1 implements conversion.Convertible interface
var (
	_ conversion.Convertible = &LLMInferenceService{}
	_ conversion.Convertible = &LLMInferenceServiceConfig{}
)

// ConvertTo converts v1alpha1 LLMInferenceService to the Hub version (v1alpha2)
func (src *LLMInferenceService) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1alpha2.LLMInferenceService)

	// Set correct APIVersion and Kind for v1alpha2
	dst.APIVersion = v1alpha2.SchemeGroupVersion.String()
	dst.Kind = "LLMInferenceService"

	// Copy ObjectMeta
	dst.ObjectMeta = src.ObjectMeta

	// Preserve criticality in annotation if it exists
	if src.Spec.Model.Criticality != nil {
		if dst.Annotations == nil {
			dst.Annotations = make(map[string]string)
		}
		dst.Annotations[CriticalityAnnotation] = string(*src.Spec.Model.Criticality)
	}

	// Convert Spec
	if err := convertSpecToV1alpha2(&src.Spec, &dst.Spec); err != nil {
		return err
	}

	// Convert Status
	convertStatusToV1alpha2(&src.Status, &dst.Status)

	return nil
}

// ConvertFrom converts from the Hub version (v1alpha2) to v1alpha1 LLMInferenceService
func (dst *LLMInferenceService) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1alpha2.LLMInferenceService)

	// Set correct APIVersion and Kind for v1alpha1
	dst.APIVersion = SchemeGroupVersion.String()
	dst.Kind = "LLMInferenceService"

	// Copy ObjectMeta
	dst.ObjectMeta = src.ObjectMeta

	// Restore criticality from annotation if it exists
	if src.Annotations != nil {
		if criticality, ok := src.Annotations[CriticalityAnnotation]; ok {
			c := Criticality(criticality)
			dst.Spec.Model.Criticality = &c
		}
	}

	// Convert Spec
	if err := convertSpecToV1alpha1(&src.Spec, &dst.Spec); err != nil {
		return err
	}

	// Convert Status
	convertStatusToV1alpha1(&src.Status, &dst.Status)

	return nil
}

// ConvertTo converts v1alpha1 LLMInferenceServiceConfig to the Hub version (v1alpha2)
func (src *LLMInferenceServiceConfig) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1alpha2.LLMInferenceServiceConfig)

	// Set correct APIVersion and Kind for v1alpha2
	dst.APIVersion = v1alpha2.SchemeGroupVersion.String()
	dst.Kind = "LLMInferenceServiceConfig"

	// Copy ObjectMeta
	dst.ObjectMeta = src.ObjectMeta

	// Preserve criticality in annotation if it exists
	if src.Spec.Model.Criticality != nil {
		if dst.Annotations == nil {
			dst.Annotations = make(map[string]string)
		}
		dst.Annotations[CriticalityAnnotation] = string(*src.Spec.Model.Criticality)
	}

	// Convert Spec
	if err := convertSpecToV1alpha2(&src.Spec, &dst.Spec); err != nil {
		return err
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1alpha2) to v1alpha1 LLMInferenceServiceConfig
func (dst *LLMInferenceServiceConfig) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1alpha2.LLMInferenceServiceConfig)

	// Set correct APIVersion and Kind for v1alpha1
	dst.APIVersion = SchemeGroupVersion.String()
	dst.Kind = "LLMInferenceServiceConfig"

	// Copy ObjectMeta
	dst.ObjectMeta = src.ObjectMeta

	// Restore criticality from annotation if it exists
	if src.Annotations != nil {
		if criticality, ok := src.Annotations[CriticalityAnnotation]; ok {
			c := Criticality(criticality)
			dst.Spec.Model.Criticality = &c
		}
	}

	// Convert Spec
	if err := convertSpecToV1alpha1(&src.Spec, &dst.Spec); err != nil {
		return err
	}

	return nil
}

// Helper function to convert Spec from v1alpha1 to v1alpha2
func convertSpecToV1alpha2(src *LLMInferenceServiceSpec, dst *v1alpha2.LLMInferenceServiceSpec) error {
	// Convert Model
	dst.Model.URI = src.Model.URI
	dst.Model.Name = src.Model.Name
	// Note: Criticality is dropped (stored in annotation by caller)

	// Convert LoRA if present
	if src.Model.LoRA != nil {
		dst.Model.LoRA = &v1alpha2.LoRASpec{
			Adapters: make([]v1alpha2.LLMModelSpec, len(src.Model.LoRA.Adapters)),
		}
		for i, adapter := range src.Model.LoRA.Adapters {
			dst.Model.LoRA.Adapters[i].URI = adapter.URI
			dst.Model.LoRA.Adapters[i].Name = adapter.Name
			// Recursively convert nested LoRA if present
			if adapter.LoRA != nil {
				dst.Model.LoRA.Adapters[i].LoRA = &v1alpha2.LoRASpec{
					Adapters: make([]v1alpha2.LLMModelSpec, len(adapter.LoRA.Adapters)),
				}
				for j, nestedAdapter := range adapter.LoRA.Adapters {
					dst.Model.LoRA.Adapters[i].LoRA.Adapters[j].URI = nestedAdapter.URI
					dst.Model.LoRA.Adapters[i].LoRA.Adapters[j].Name = nestedAdapter.Name
				}
			}
		}
	}

	// Convert WorkloadSpec
	convertWorkloadSpecToV1alpha2(&src.WorkloadSpec, &dst.WorkloadSpec)

	// Convert Router if present
	if src.Router != nil {
		dst.Router = &v1alpha2.RouterSpec{}
		if err := convertRouterSpecToV1alpha2(src.Router, dst.Router); err != nil {
			return err
		}
	}

	// Convert Prefill if present
	if src.Prefill != nil {
		dst.Prefill = &v1alpha2.WorkloadSpec{}
		convertWorkloadSpecToV1alpha2(src.Prefill, dst.Prefill)
	}

	// Convert BaseRefs
	dst.BaseRefs = src.BaseRefs

	return nil
}

// Helper function to convert Spec from v1alpha2 to v1alpha1
func convertSpecToV1alpha1(src *v1alpha2.LLMInferenceServiceSpec, dst *LLMInferenceServiceSpec) error {
	// Convert Model
	dst.Model.URI = src.Model.URI
	dst.Model.Name = src.Model.Name
	// Note: Criticality is restored from annotation by caller

	// Convert LoRA if present
	if src.Model.LoRA != nil {
		dst.Model.LoRA = &LoRASpec{
			Adapters: make([]LLMModelSpec, len(src.Model.LoRA.Adapters)),
		}
		for i, adapter := range src.Model.LoRA.Adapters {
			dst.Model.LoRA.Adapters[i].URI = adapter.URI
			dst.Model.LoRA.Adapters[i].Name = adapter.Name
			// Recursively convert nested LoRA if present
			if adapter.LoRA != nil {
				dst.Model.LoRA.Adapters[i].LoRA = &LoRASpec{
					Adapters: make([]LLMModelSpec, len(adapter.LoRA.Adapters)),
				}
				for j, nestedAdapter := range adapter.LoRA.Adapters {
					dst.Model.LoRA.Adapters[i].LoRA.Adapters[j].URI = nestedAdapter.URI
					dst.Model.LoRA.Adapters[i].LoRA.Adapters[j].Name = nestedAdapter.Name
				}
			}
		}
	}

	// Convert WorkloadSpec
	convertWorkloadSpecToV1alpha1(&src.WorkloadSpec, &dst.WorkloadSpec)

	// Convert Router if present
	if src.Router != nil {
		dst.Router = &RouterSpec{}
		if err := convertRouterSpecToV1alpha1(src.Router, dst.Router); err != nil {
			return err
		}
	}

	// Convert Prefill if present
	if src.Prefill != nil {
		dst.Prefill = &WorkloadSpec{}
		convertWorkloadSpecToV1alpha1(src.Prefill, dst.Prefill)
	}

	// Convert BaseRefs
	dst.BaseRefs = src.BaseRefs

	return nil
}

func convertWorkloadSpecToV1alpha2(src *WorkloadSpec, dst *v1alpha2.WorkloadSpec) {
	dst.Replicas = src.Replicas

	if src.Parallelism != nil {
		dst.Parallelism = &v1alpha2.ParallelismSpec{
			Tensor:      src.Parallelism.Tensor,
			Pipeline:    src.Parallelism.Pipeline,
			Data:        src.Parallelism.Data,
			DataLocal:   src.Parallelism.DataLocal,
			DataRPCPort: src.Parallelism.DataRPCPort,
			Expert:      src.Parallelism.Expert,
		}
	}

	dst.Template = src.Template
	dst.Worker = src.Worker
}

func convertWorkloadSpecToV1alpha1(src *v1alpha2.WorkloadSpec, dst *WorkloadSpec) {
	dst.Replicas = src.Replicas

	if src.Parallelism != nil {
		dst.Parallelism = &ParallelismSpec{
			Tensor:      src.Parallelism.Tensor,
			Pipeline:    src.Parallelism.Pipeline,
			Data:        src.Parallelism.Data,
			DataLocal:   src.Parallelism.DataLocal,
			DataRPCPort: src.Parallelism.DataRPCPort,
			Expert:      src.Parallelism.Expert,
		}
	}

	dst.Template = src.Template
	dst.Worker = src.Worker
}

func convertRouterSpecToV1alpha2(src *RouterSpec, dst *v1alpha2.RouterSpec) error {
	// Convert Route
	if src.Route != nil {
		dst.Route = &v1alpha2.GatewayRoutesSpec{}
		if src.Route.HTTP != nil {
			dst.Route.HTTP = &v1alpha2.HTTPRouteSpec{
				Refs: src.Route.HTTP.Refs,
				Spec: src.Route.HTTP.Spec,
			}
		}
	}

	// Convert Gateway
	if src.Gateway != nil {
		dst.Gateway = &v1alpha2.GatewaySpec{
			Refs: make([]v1alpha2.UntypedObjectReference, len(src.Gateway.Refs)),
		}
		for i, ref := range src.Gateway.Refs {
			dst.Gateway.Refs[i] = v1alpha2.UntypedObjectReference{
				Name:      ref.Name,
				Namespace: ref.Namespace,
			}
		}
	}

	// Convert Ingress
	if src.Ingress != nil {
		dst.Ingress = &v1alpha2.IngressSpec{
			Refs: make([]v1alpha2.UntypedObjectReference, len(src.Ingress.Refs)),
		}
		for i, ref := range src.Ingress.Refs {
			dst.Ingress.Refs[i] = v1alpha2.UntypedObjectReference{
				Name:      ref.Name,
				Namespace: ref.Namespace,
			}
		}
	}

	// Convert Scheduler
	if src.Scheduler != nil {
		dst.Scheduler = &v1alpha2.SchedulerSpec{
			Template: src.Scheduler.Template,
		}

		if src.Scheduler.Pool != nil {
			dst.Scheduler.Pool = &v1alpha2.InferencePoolSpec{}

			// Convert Spec if present
			if src.Scheduler.Pool.Spec != nil {
				convertedSpec := convertGIEInferencePoolSpecToV1(src.Scheduler.Pool.Spec)
				dst.Scheduler.Pool.Spec = convertedSpec
			}

			// Convert Ref
			dst.Scheduler.Pool.Ref = src.Scheduler.Pool.Ref
		}
	}

	return nil
}

func convertRouterSpecToV1alpha1(src *v1alpha2.RouterSpec, dst *RouterSpec) error {
	// Convert Route
	if src.Route != nil {
		dst.Route = &GatewayRoutesSpec{}
		if src.Route.HTTP != nil {
			dst.Route.HTTP = &HTTPRouteSpec{
				Refs: src.Route.HTTP.Refs,
				Spec: src.Route.HTTP.Spec,
			}
		}
	}

	// Convert Gateway
	if src.Gateway != nil {
		dst.Gateway = &GatewaySpec{
			Refs: make([]UntypedObjectReference, len(src.Gateway.Refs)),
		}
		for i, ref := range src.Gateway.Refs {
			dst.Gateway.Refs[i] = UntypedObjectReference{
				Name:      ref.Name,
				Namespace: ref.Namespace,
			}
		}
	}

	// Convert Ingress
	if src.Ingress != nil {
		dst.Ingress = &IngressSpec{
			Refs: make([]UntypedObjectReference, len(src.Ingress.Refs)),
		}
		for i, ref := range src.Ingress.Refs {
			dst.Ingress.Refs[i] = UntypedObjectReference{
				Name:      ref.Name,
				Namespace: ref.Namespace,
			}
		}
	}

	// Convert Scheduler
	if src.Scheduler != nil {
		dst.Scheduler = &SchedulerSpec{
			Template: src.Scheduler.Template,
		}

		if src.Scheduler.Pool != nil {
			dst.Scheduler.Pool = &InferencePoolSpec{}

			// Convert Spec if present
			if src.Scheduler.Pool.Spec != nil {
				convertedSpec := convertV1InferencePoolSpecToGIE(src.Scheduler.Pool.Spec)
				dst.Scheduler.Pool.Spec = convertedSpec
			}

			// Convert Ref
			dst.Scheduler.Pool.Ref = src.Scheduler.Pool.Ref
		}
	}

	return nil
}

func convertStatusToV1alpha2(src *LLMInferenceServiceStatus, dst *v1alpha2.LLMInferenceServiceStatus) {
	dst.URL = src.URL
	dst.Status = src.Status
	dst.AddressStatus = src.AddressStatus
}

func convertStatusToV1alpha1(src *v1alpha2.LLMInferenceServiceStatus, dst *LLMInferenceServiceStatus) {
	dst.URL = src.URL
	dst.Status = src.Status
	dst.AddressStatus = src.AddressStatus
}

// convertGIEInferencePoolSpecToV1 converts v1alpha1 GIEInferencePoolSpec to v1 InferencePoolSpec
func convertGIEInferencePoolSpecToV1(src *GIEInferencePoolSpec) *igwv1.InferencePoolSpec {
	if src == nil {
		return nil
	}

	dst := &igwv1.InferencePoolSpec{
		Selector: igwv1.LabelSelector{
			MatchLabels: src.Selector,
		},
		TargetPorts: []igwv1.Port{
			{
				Number: igwv1.PortNumber(src.TargetPortNumber),
			},
		},
	}

	// Convert EndpointPickerConfig
	if src.ExtensionRef != nil {
		dst.EndpointPickerRef.Group = src.ExtensionRef.Group
		if src.ExtensionRef.Kind != nil {
			dst.EndpointPickerRef.Kind = *src.ExtensionRef.Kind
		}
		dst.EndpointPickerRef.Name = src.ExtensionRef.Name

		if src.ExtensionRef.PortNumber != nil {
			portNumber := *src.ExtensionRef.PortNumber
			dst.EndpointPickerRef.Port = &igwv1.Port{
				Number: portNumber,
			}
		}

		if src.ExtensionRef.FailureMode != nil {
			failureMode := igwv1.EndpointPickerFailureMode(*src.ExtensionRef.FailureMode)
			dst.EndpointPickerRef.FailureMode = failureMode
		}
	}

	return dst
}

// convertV1InferencePoolSpecToGIE converts v1 InferencePoolSpec to v1alpha1 GIEInferencePoolSpec
func convertV1InferencePoolSpecToGIE(src *igwv1.InferencePoolSpec) *GIEInferencePoolSpec {
	if src == nil {
		return nil
	}

	dst := &GIEInferencePoolSpec{
		Selector: src.Selector.MatchLabels,
	}

	// Convert TargetPorts - we only support single port
	if len(src.TargetPorts) > 0 {
		dst.TargetPortNumber = int32(src.TargetPorts[0].Number)
	}

	// Convert EndpointPickerRef
	dst.EndpointPickerConfig.ExtensionRef = &Extension{
		ExtensionReference: ExtensionReference{
			Group: src.EndpointPickerRef.Group,
			Kind:  ptr.To(src.EndpointPickerRef.Kind),
			Name:  src.EndpointPickerRef.Name,
		},
	}

	if src.EndpointPickerRef.Port != nil {
		portNumber := src.EndpointPickerRef.Port.Number
		dst.EndpointPickerConfig.ExtensionRef.PortNumber = &portNumber
	}

	if src.EndpointPickerRef.FailureMode != "" {
		failureMode := ExtensionFailureMode(src.EndpointPickerRef.FailureMode)
		dst.EndpointPickerConfig.ExtensionRef.ExtensionConnection.FailureMode = &failureMode
	}

	return dst
}

// MarshalJSON is a custom JSON marshaler to handle conversion
func (c *Criticality) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(*c))
}

// UnmarshalJSON is a custom JSON unmarshaler to handle conversion
func (c *Criticality) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*c = Criticality(s)
	return nil
}
