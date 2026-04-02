//go:build distro

/*
Copyright 2021 The KServe Authors.

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

package v1beta1

// OauthConfig holds OAuth proxy configuration for OCP deployments.
// The distro build tag keeps this type out of non-distro builds entirely -
// not because openapi-gen would pick it up (it won't, since OauthConfig is
// never a field in a registered API type), but because an _ocp.go file that
// compiles in upstream builds would be confusing and defeats the convention.
//
// +kubebuilder:object:generate=false
type OauthConfig struct {
	Image                  string `json:"image"`
	CpuLimit               string `json:"cpuLimit"`
	CpuRequest             string `json:"cpuRequest"`
	MemoryLimit            string `json:"memoryLimit"`
	MemoryRequest          string `json:"memoryRequest"`
	UpstreamTimeoutSeconds string `json:"upstreamTimeoutSeconds,omitempty"`
}
