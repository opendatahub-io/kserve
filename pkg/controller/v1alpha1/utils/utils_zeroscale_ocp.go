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

package utils

import (
	"github.com/go-logr/logr"
	"knative.dev/serving/pkg/apis/autoscaling"

	"github.com/kserve/kserve/pkg/constants"
)

// ValidateInitialScaleAnnotationWithReplicas wraps ValidateInitialScaleAnnotation and additionally
// sets the initial scale annotation to 0 when zero min replicas are requested but the annotation
// is not explicitly set by the user.
func ValidateInitialScaleAnnotationWithReplicas(annotations map[string]string, allowZeroInitialScale bool, minReplicas *int32, log logr.Logger) {
	_, set := annotations[autoscaling.InitialScaleAnnotationKey]
	if !set && allowZeroInitialScale {
		if minReplicas == nil && constants.DefaultMinReplicas == 0 {
			annotations[autoscaling.InitialScaleAnnotationKey] = "0"
		}
		if minReplicas != nil && *minReplicas == 0 {
			annotations[autoscaling.InitialScaleAnnotationKey] = "0"
		}
	}

	ValidateInitialScaleAnnotation(annotations, allowZeroInitialScale, log)
}
