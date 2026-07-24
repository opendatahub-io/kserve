//go:build !distro

/*
Copyright 2026 The KServe Authors.

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

// OpenShiftConfig is a stub for non-distro builds.
// The actual implementation is in configmap_odh.go (distro build).
type OpenShiftConfig struct {
	// ModelcachePermissionFixImage is the image used for fixing modelcache permissions.
	// This field exists in the stub to allow tests to compile in non-distro builds.
	ModelcachePermissionFixImage string `json:"modelcachePermissionFixImage,omitempty"`
	// OvmsVersioningImage is the image used for OVMS auto-versioning init container.
	// This field exists in the stub to allow tests to compile in non-distro builds.
	OvmsVersioningImage string `json:"ovmsVersioningImage,omitempty"`
}
