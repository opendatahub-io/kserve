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
)

func (in *GatewayRoutesSpec) IsManaged() bool {
	return in != nil && in == &GatewayRoutesSpec{}
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
	return p != nil && p.Pipeline != nil && *p.Pipeline > 0
}

func (p *ParallelismSpec) IsDataParallel() bool {
	return p != nil && ((p.Data != nil && *p.Data > 0) || (p.DataLocal != nil && *p.DataLocal > 0))
}

func (p *ParallelismSpec) IsTensorParallel() bool {
	return p != nil && p.Tensor != nil && *p.Tensor > 0
}

func (p *ParallelismSpec) GetSize() *int32 {
	if p == nil {
		return nil
	}
	if p.IsDataParallel() {
		return ptr.To(max(getOrDefault(p.Data, 1), 1) / max(getOrDefault(p.DataLocal, 1), 1))
	}
	if p.IsPipelineParallel() {
		return p.Pipeline
	}
	return nil
}

func getOrDefault(value *int32, defaultValue int32) int32 {
	if value == nil {
		return defaultValue
	}
	return *value
}
