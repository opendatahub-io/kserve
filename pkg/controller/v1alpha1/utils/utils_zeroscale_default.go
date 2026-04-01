//go:build !distro

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

package utils

import (
	"github.com/go-logr/logr"
)

// ValidateInitialScaleAnnotationWithReplicas delegates to ValidateInitialScaleAnnotation in upstream builds.
// The minReplicas parameter is accepted for API compatibility but is unused.
// The returned map must be assigned back by the caller (mirrors the distro signature).
func ValidateInitialScaleAnnotationWithReplicas(annotations map[string]string, allowZeroInitialScale bool, _ *int32, log logr.Logger) map[string]string {
	ValidateInitialScaleAnnotation(annotations, allowZeroInitialScale, log)
	return annotations
}
