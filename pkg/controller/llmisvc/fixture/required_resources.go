/*
Copyright 2025 The KServe Authors.

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

package fixture

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/testing"

	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"
)

func RequiredResources(ctx context.Context, c client.Client, ns string) {
	gomega.Expect(c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
		},
	})).To(gomega.Succeed())

	SharedConfigPresets(ctx, c, ns)
	InferenceServiceCfgMap(ctx, c, ns)
	DefaultGateway(ctx, c, ns)
}

// DefaultGateway creates Gateway instance derived from charts/kserve-resources/templates/ingress_gateway.yaml
func DefaultGateway(ctx context.Context, c client.Client, ns string) {
	gw := Gateway("kserve-ingress-gateway",
		InNamespace[*gatewayapiv1.Gateway](ns),
		WithClassName("istio"),
		WithListeners(gatewayapiv1.Listener{
			Name:     "http",
			Port:     80,
			Protocol: gatewayapiv1.HTTPProtocolType,
			AllowedRoutes: &gatewayapiv1.AllowedRoutes{
				Namespaces: &gatewayapiv1.RouteNamespaces{
					From: ptr.To(gatewayapiv1.NamespacesFromAll),
				},
			},
		}),
	)

	gw.Spec.Infrastructure = &gatewayapiv1.GatewayInfrastructure{
		Labels: map[gatewayapiv1.LabelKey]gatewayapiv1.LabelValue{
			"serving.kserve.io/gateway": "kserve-ingress-gateway",
		},
	}

	gomega.Expect(c.Create(ctx, gw)).To(gomega.Succeed())
}

func InferenceServiceCfgMap(ctx context.Context, c client.Client, ns string) {
	configs := map[string]string{
		"ingress": `{
				"enableGatewayApi": true,
				"kserveIngressGateway": "kserve/kserve-ingress-gateway",
				"ingressGateway": "knative-serving/knative-ingress-gateway",
				"localGateway": "knative-serving/knative-local-gateway",
				"localGatewayService": "knative-local-gateway.istio-system.svc.cluster.local",
				"additionalIngressDomains": ["additional.example.com"]
			}`,
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.InferenceServiceConfigMapName,
			Namespace: ns,
		},
		Data: configs,
	}
	gomega.Expect(c.Create(ctx, configMap)).NotTo(gomega.HaveOccurred())
}

// SharedConfigPresets loads preset files shared as kustomize manifests that are stored in projects config.
// Every file prefixed with `config-` is treated as such
func SharedConfigPresets(ctx context.Context, c client.Client, ns string) {
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

	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}
