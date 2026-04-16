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

//go:build !distro

package inferenceservice

import (
	"context"
	"fmt"

	. "github.com/onsi/gomega"
	netv1 "k8s.io/api/networking/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

func assertPlatformIngressExists(ctx context.Context, serviceKey types.NamespacedName) {
	Eventually(func() error {
		return k8sClient.Get(ctx, serviceKey, &netv1.Ingress{})
	}, timeout, interval).Should(Succeed())
}

func assertPlatformIngressDoesNotExist(ctx context.Context, serviceKey types.NamespacedName) {
	Consistently(func() bool {
		err := k8sClient.Get(ctx, serviceKey, &netv1.Ingress{})
		return apierr.IsNotFound(err)
	}, fastTimeout, interval).Should(BeTrue(), "Ingress %s should not exist", serviceKey.Name)
}

func assertPlatformIngressDeleted(ctx context.Context, serviceKey types.NamespacedName) {
	Eventually(func() bool {
		err := k8sClient.Get(ctx, serviceKey, &netv1.Ingress{})
		return apierr.IsNotFound(err)
	}, timeout, interval).Should(BeTrue(), "Ingress %s should be deleted", serviceKey.Name)
}

func assertPlatformIngressSpec(ctx context.Context, serviceKey types.NamespacedName, serviceName string, _ *v1beta1.InferenceService) {
	pathType := netv1.PathTypePrefix
	actualIngress := &netv1.Ingress{}
	Eventually(func() error {
		return k8sClient.Get(ctx, serviceKey, actualIngress)
	}, timeout, interval).Should(Succeed())

	expectedIngress := netv1.Ingress{
		Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{
				{
					Host: fmt.Sprintf("%s-%s.%s", serviceName, serviceKey.Namespace, domain),
					IngressRuleValue: netv1.IngressRuleValue{
						HTTP: &netv1.HTTPIngressRuleValue{
							Paths: []netv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: netv1.IngressBackend{
										Service: &netv1.IngressServiceBackend{
											Name: serviceName + "-predictor",
											Port: netv1.ServiceBackendPort{
												Number: 80,
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Host: fmt.Sprintf("%s-predictor-%s.%s", serviceName, serviceKey.Namespace, domain),
					IngressRuleValue: netv1.IngressRuleValue{
						HTTP: &netv1.HTTPIngressRuleValue{
							Paths: []netv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: netv1.IngressBackend{
										Service: &netv1.IngressServiceBackend{
											Name: serviceName + "-predictor",
											Port: netv1.ServiceBackendPort{
												Number: 80,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	Expect(actualIngress.Spec).To(Equal(expectedIngress.Spec))
}

func simulateIngressAdmission(_ context.Context, _ types.NamespacedName, _ string) {}

func expectedIsvcStatusURL(serviceName, namespace string) (string, string) {
	return "http", fmt.Sprintf("%s-%s.%s", serviceName, namespace, domain)
}
