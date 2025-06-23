/*
Copyright 2023 The KServe Authors.

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

package llmisvc_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	. "github.com/onsi/gomega"
)

func createRequiredResources(ctx context.Context, c client.Client, ns string) {
	Expect(envTest.Client.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
		},
	})).To(Succeed())

	createSharedConfigPresets(ctx, c, ns)
}

// createSharedConfigPresets loads preset files shared as kustomize manifests that are stored in projects config.
// Every file prefixed with `config-` is treated as such
func createSharedConfigPresets(ctx context.Context, c client.Client, ns string) {
	configDir := filepath.Join(testing.ProjectRoot(), "config", "llmisvc")
	err := filepath.Walk(configDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") || !strings.HasPrefix(info.Name(), "config-") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		config := &v1alpha1.LLMInferenceServiceConfig{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
			},
		}
		if err := yaml.Unmarshal(data, config); err != nil {
			return err
		}

		return c.Create(ctx, config)
	})

	Expect(err).NotTo(HaveOccurred())

	baseTemplateConfig := &v1alpha1.LLMInferenceServiceConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kserve-config-llm-template",
			Namespace: "kserve",
		},

		Spec: v1alpha1.LLMInferenceServiceSpec{
			WorkloadSpec: v1alpha1.WorkloadSpec{
				Template: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: "facebook/opt-125m:latest",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									"nvidia.com/gpu": resource.MustParse("1"),
								},
							},
						},
					},
				},
			},
		},
	}
	Expect(c.Create(ctx, baseTemplateConfig)).To(Succeed())
}
