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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakeclientset "k8s.io/client-go/kubernetes/fake"

	"github.com/kserve/kserve/pkg/constants"
)

func TestGetInferenceServiceConfigMapInNamespace(t *testing.T) {
	const namespace = "applications"
	clientset := fakeclientset.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.InferenceServiceConfigMapName,
			Namespace: namespace,
		},
	})

	configMap, err := GetInferenceServiceConfigMapInNamespace(t.Context(), clientset, namespace)
	if err != nil {
		t.Fatalf("GetInferenceServiceConfigMapInNamespace() error = %v", err)
	}
	if configMap.Namespace != namespace {
		t.Fatalf("ConfigMap namespace = %q, want %q", configMap.Namespace, namespace)
	}
}
