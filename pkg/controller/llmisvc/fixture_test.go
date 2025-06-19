package llmisvc_test

import (
	"context"
	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func sharedTestFixture(ctx context.Context, c client.Client) {
	Expect(envTest.Client.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kserve",
		},
	})).To(Succeed())

	templateConfig := &v1alpha1.LLMInferenceServiceConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kserve-config-llm-template",
			Namespace: "kserve",
		},
		Spec: v1alpha1.LLMInferenceServiceSpec{
			WorkloadSpec: v1alpha1.WorkloadSpec{
				Template: &corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "model",
							Image: "test-model:latest",
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
	Expect(c.Create(ctx, templateConfig)).To(Succeed())
}
