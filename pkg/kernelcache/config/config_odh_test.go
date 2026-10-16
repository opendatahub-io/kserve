//go:build distro

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

package config

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	controllerutils "github.com/kserve/kserve/pkg/controller/v1alpha1/utils"
)

func TestLoadUsesConfiguredNamespace(t *testing.T) {
	t.Setenv(controllerutils.KServeConfigNamespaceEnv, "applications")
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inferenceservice-config",
			Namespace: "applications",
		},
		Data: map[string]string{v1beta1.KernelCacheConfigName: `{"enabled":true}`},
	}).Build()

	loaded, err := Load(context.Background(), reader)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.Enabled {
		t.Fatal("Load() returned disabled configuration")
	}
}
