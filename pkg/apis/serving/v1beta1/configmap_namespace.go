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

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kserve/kserve/pkg/constants"
)

// GetInferenceServiceConfigMapInNamespace gets the shared configuration
// ConfigMap from the supplied namespace. Callers that run outside the
// applications namespace use this helper with their configured source
// namespace; callers that use the default KServe namespace can continue to
// use GetInferenceServiceConfigMap.
func GetInferenceServiceConfigMapInNamespace(ctx context.Context, clientset kubernetes.Interface, namespace string) (*corev1.ConfigMap, error) {
	return clientset.CoreV1().ConfigMaps(namespace).Get(ctx, constants.InferenceServiceConfigMapName, metav1.GetOptions{})
}
