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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"knative.dev/pkg/kmeta"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/llmisvc"

	"github.com/kserve/kserve/pkg/testing"

	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"
)

const (
	defaultGatewayClass = "istio"
)

func RequiredResources(ctx context.Context, c client.Client, ns string) {
	gomega.Expect(c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
		},
	})).To(gomega.Succeed())

	// Create namespace for CA signing secret
	gomega.Expect(c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: llmisvc.ServiceCASigningSecretNamespace,
		},
	})).To(gomega.Succeed())

	gomega.Expect(c.Create(ctx, InferenceServiceCfgMap(ns))).To(gomega.Succeed())

	for _, preset := range SharedConfigPresets(ns) {
		gomega.Expect(c.Create(ctx, preset)).To(gomega.Succeed())
	}

	gomega.Expect(c.Create(ctx, DefaultGateway(ns))).To(gomega.Succeed())
	gomega.Expect(c.Create(ctx, DefaultGatewayClass())).To(gomega.Succeed())
	gomega.Expect(c.Create(ctx, SigningKey(llmisvc.ServiceCASigningSecretNamespace, llmisvc.ServiceCASigningSecretName))).To(gomega.Succeed())
}

func IstioShadowService(name, ns string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "istio-shadow",
			Namespace: ns,
			Labels: map[string]string{
				"istio.io/inferencepool-name": kmeta.ChildName(name, "-inference-pool"),
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.IntOrString{IntVal: 8000},
				},
				{
					Name:       "https",
					Port:       443,
					TargetPort: intstr.IntOrString{IntVal: 8001},
				},
			},
		},
	}
}

func DefaultServiceAccount(ns string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: ns,
		},
	}
}

func DefaultGateway(ns string) *gatewayapiv1.Gateway {
	defaultGateway := Gateway(constants.GatewayName,
		InNamespace[*gatewayapiv1.Gateway](ns),
		WithClassName(defaultGatewayClass),
		WithInfrastructureLabels("serving.kserve.io/gateway", constants.GatewayName),
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

	return defaultGateway
}

func DefaultGatewayClass() *gatewayapiv1.GatewayClass {
	return &gatewayapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaultGatewayClass,
		},
		Spec: gatewayapiv1.GatewayClassSpec{
			ControllerName: "istio.io/gateway-controller",
		},
	}
}

func InferenceServiceCfgMap(ns string) *corev1.ConfigMap {
	configs := map[string]string{
		"ingress": `{
				"enableGatewayApi": true,
				"kserveIngressGateway": "kserve/kserve-ingress-gateway",
				"ingressGateway": "knative-serving/knative-ingress-gateway",
				"localGateway": "knative-serving/knative-local-gateway",
				"localGatewayService": "knative-local-gateway.istio-system.svc.cluster.local",
				"additionalIngressDomains": ["additional.example.com"]
			}`,
		"storageInitializer": `{
				"memoryRequest": "100Mi",
				"memoryLimit": "1Gi",
				"cpuRequest": "100m",
				"cpuLimit": "1",
				"cpuModelcar": "10m",
				"memoryModelcar": "15Mi",
				"enableModelcar": true
			}`,
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constants.InferenceServiceConfigMapName,
			Namespace: ns,
		},
		Data: configs,
	}

	return configMap
}

// testGlobalConfig provides test values for GlobalConfig template variables.
var testGlobalConfig = &llmisvc.Config{
	SystemNamespace:         constants.KServeNamespace,
	IngressGatewayName:      constants.GatewayName,
	IngressGatewayNamespace: constants.KServeNamespace,
}

// SharedConfigPresets loads preset files shared as kustomize manifests that are stored in projects config.
// Every file prefixed with `config-` is treated as such.
// Templates are processed with test values to pass CRD validation in envtest.
func SharedConfigPresets(ns string) []*v1alpha2.LLMInferenceServiceConfig {
	configDir := filepath.Join(testing.ProjectRoot(), "config", "llmisvc")
	var configs []*v1alpha2.LLMInferenceServiceConfig
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

		// Process Go templates with test values
		processedData := processConfigTemplates(data)

		config := &v1alpha2.LLMInferenceServiceConfig{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
			},
		}
		if err := yaml.Unmarshal(processedData, config); err != nil {
			return err
		}

		configs = append(configs, config)

		return nil
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	return configs
}

// testServiceData provides placeholder values for service-specific templates during test setup.
// These values are used to pass CRD validation when loading shared config presets.
// The controller will NOT call ReplaceVariables on these already-processed configs,
// but tests verify the controller's behavior through actual reconciliation outcomes.
var testServiceData = struct {
	*v1alpha2.LLMInferenceService
	GlobalConfig *llmisvc.Config
}{
	LLMInferenceService: &v1alpha2.LLMInferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "test-namespace",
		},
		Spec: v1alpha2.LLMInferenceServiceSpec{
			Model: v1alpha2.LLMModelSpec{
				Name: ptr.To("test-model"),
			},
			WorkloadSpec: v1alpha2.WorkloadSpec{
				Parallelism: &v1alpha2.ParallelismSpec{
					Tensor:      ptr.To(int32(1)),
					Data:        ptr.To(int32(1)),
					DataLocal:   ptr.To(int32(1)),
					DataRPCPort: ptr.To(int32(8080)),
					Expert:      false,
				},
			},
			Prefill: &v1alpha2.WorkloadSpec{
				Parallelism: &v1alpha2.ParallelismSpec{
					Tensor:      ptr.To(int32(1)),
					Data:        ptr.To(int32(1)),
					DataLocal:   ptr.To(int32(1)),
					DataRPCPort: ptr.To(int32(8080)),
					Expert:      false,
				},
			},
		},
	},
	GlobalConfig: testGlobalConfig,
}

// processConfigTemplates processes ONLY templates that would fail CRD validation.
// The HTTPRoute path.value fields have regex validation that disallows {{ }} characters,
// so we must process templates in those fields. Other templates (like backendRefs.name)
// are preserved for the controller's ReplaceVariables function to process at runtime.
//
// Fields processed:
// - spec.router.route.http.spec.parentRefs[*].name
// - spec.router.route.http.spec.parentRefs[*].namespace
// - spec.router.route.http.spec.rules[*].matches[*].path.value
//
// Fields preserved for ReplaceVariables:
// - spec.router.route.http.spec.rules[*].backendRefs[*].name
func processConfigTemplates(data []byte) []byte {
	if !strings.Contains(string(data), "{{") {
		return data
	}

	// Only process specific templates that fail CRD validation
	// GlobalConfig templates (gateway name/namespace) and path.value templates
	result := string(data)

	// Process GlobalConfig templates for parentRefs
	result = strings.ReplaceAll(result, "{{ .GlobalConfig.IngressGatewayName }}", testGlobalConfig.IngressGatewayName)
	result = strings.ReplaceAll(result, "{{ .GlobalConfig.IngressGatewayNamespace }}", testGlobalConfig.IngressGatewayNamespace)

	// Process path.value templates - these have CRD regex validation
	// Pattern: /{{ .ObjectMeta.Namespace }}/{{ .ObjectMeta.Name }}/...
	result = strings.ReplaceAll(result, "{{ .ObjectMeta.Namespace }}", testServiceData.ObjectMeta.Namespace)
	result = strings.ReplaceAll(result, "{{ .ObjectMeta.Name }}", testServiceData.ObjectMeta.Name)

	return []byte(result)
}

// SigningKey generates a mock CA certificate and private key for testing purposes.
// This creates a self-signed CA certificate that can be used to sign other certificates in envtests.
func SigningKey(namespace, name string) *corev1.Secret {
	// Generate CA private key (RSA 4096-bit for FIPS compliance)
	caPrivateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Generate serial number for CA certificate
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Create CA certificate template
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Test CA"},
			CommonName:   "Test CA for KServe",
		},
		NotBefore:             now.UTC(),
		NotAfter:              now.Add(365 * 24 * time.Hour).UTC(), // 1 year validity for test CA
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
		SignatureAlgorithm:    x509.SHA256WithRSA, // FIPS-approved signature algorithm
	}

	// Create self-signed CA certificate
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivateKey.PublicKey, caPrivateKey)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Encode CA certificate to PEM
	caCertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCertDER,
	})

	// Encode CA private key to PEM (PKCS#8 format for FIPS compliance)
	caPrivateKeyBytes, err := x509.MarshalPKCS8PrivateKey(caPrivateKey)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	caPrivateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: caPrivateKeyBytes,
	})

	// Create the secret
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": caCertPEM,
			"tls.key": caPrivateKeyPEM,
		},
	}
}
