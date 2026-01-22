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
	"fmt"
	"strings"

	"k8s.io/utils/ptr"
	"knative.dev/pkg/kmeta"
)

func (s *SchedulerSpec) InferencePoolName(llmSvc *LLMInferenceService) string {
	if s == nil || s.Pool == nil || !s.Pool.HasRef() {
		// This default MUST match the default value set in the well-known presets.
		return kmeta.ChildName(llmSvc.GetName(), "-inference-pool")
	}
	return s.Pool.Ref.Name
}

func (r *RouterSpec) EPPServiceName(llmSvc *LLMInferenceService) string {
	if r == nil || r.Route == nil || r.Scheduler == nil || r.Scheduler.Pool == nil || !r.Scheduler.Pool.HasRef() || r.Scheduler.Pool.Spec == nil || r.Scheduler.Pool.Spec.ExtensionRef == nil {
		return kmeta.ChildName(llmSvc.GetName(), "-epp-service")
	}
	return string(r.Scheduler.Pool.Spec.ExtensionRef.Name)
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

func (s *LLMInferenceService) IsUsingLLMInferenceServiceConfig(name string) bool {
	for _, value := range s.Status.Annotations {
		if strings.Contains(value, name) {
			return true
		}
	}

	for _, ref := range s.Spec.BaseRefs {
		if ref.Name == name {
			return true
		}
	}

	return false
}

// String returns a human-readable representation of ParallelismSpec as JSON.
//
// Uses value receiver intentionally (unlike other methods) so both ParallelismSpec and
// *ParallelismSpec implement fmt.Stringer. This is required because K8s validation library
// (k8s.io/apimachinery/pkg/util/validation/field) dereferences pointers before checking
// for Stringer interface. With a pointer receiver, only *ParallelismSpec would implement
// Stringer, and after dereferencing, the Stringer check would fail, falling back to %#v
// which produces unhelpful output with pointer addresses.
//
// The recvcheck linter is configured to allow this exception in .golangci.yml.
// This method can be removed when upgrading to apimachinery v0.34+ which handles
// this by marshalling to JSON automatically.
func (p ParallelismSpec) String() string {
	if ptr.AllPtrFieldsNil(&p) && !p.Expert {
		return "{}"
	}
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Sprintf("%#v", p)
	}
	return string(b)
}
