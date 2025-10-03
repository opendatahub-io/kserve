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
	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
)

func (in *LLMInferenceService) InferencePoolName() string {
	s := &SchedulerSpec{}
	if in.Spec.Router != nil && in.Spec.Router.Scheduler != nil {
		s = in.Spec.Router.Scheduler
	}

	if s.Pool == nil || !s.Pool.HasRef() {
		// This default MUST match the default value set in the well-known presets.
		return in.DefaultInferencePoolName()
	}
	return s.Pool.Ref.Name
}

func (in *LLMInferenceService) DefaultInferencePoolName() string {
	return kmeta.ChildName(in.GetName(), "-inference-pool")
}

func (in *LLMInferenceService) EPPServiceName() string {
	s := &SchedulerSpec{}
	if in.Spec.Router != nil && in.Spec.Router.Scheduler != nil {
		s = in.Spec.Router.Scheduler
	}

	if s.Pool == nil || !s.Pool.HasRef() || s.Pool.Spec == nil || s.Pool.Spec.ExtensionRef == nil {
		return kmeta.ChildName(in.GetName(), "-epp-service")
	}
	return string(s.Pool.Spec.ExtensionRef.Name)
}

func (in *GatewaySpec) HasRefs() bool {
	return in != nil && len(in.Refs) > 0
}

func (r *HTTPRouteSpec) HasRefs() bool {
	return r != nil && len(r.Refs) > 0
}

func (r *HTTPRouteSpec) HasSpec() bool {
	return r != nil && r.Spec != nil
}

func (p *InferencePoolSpec) HasRef() bool {
	return p != nil && p.Ref != nil && p.Ref.Name != ""
}

func (p *ParallelismSpec) IsPipelineParallel() bool {
	if p == nil {
		return false
	}
	return ptr.Deref(p.Pipeline, 0) > 0
}

func (p *ParallelismSpec) IsDataParallel() bool {
	if p == nil {
		return false
	}
	return ptr.Deref(p.Data, 0) > 0 || ptr.Deref(p.DataLocal, 0) > 0
}

func (p *ParallelismSpec) IsTensorParallel() bool {
	if p == nil {
		return false
	}
	return ptr.Deref(p.Tensor, 0) > 0
}

func (p *ParallelismSpec) GetSize() *int32 {
	if p == nil {
		return nil
	}
	if p.IsDataParallel() {
		return ptr.To(max(
			// p.Data / p.DataLocal
			max(ptr.Deref(p.Data, 1), 1)/max(ptr.Deref(p.DataLocal, 1), 1),
			1,
		))
	}
	if p.IsPipelineParallel() {
		return p.Pipeline
	}
	return nil
}
