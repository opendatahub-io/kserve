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

package inferenceservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

var _ = Describe("RawDeployment audit logging", func() {
	BeforeEach(func() {
		configureAuditLoggingEnvTestKubeconfig()
	})

	DescribeTable("renders the effective audit setting and keeps it stable across global changes",
		func(serviceName string, globalEnabled bool, override *string, auditEnabled bool, wantAnnotation *string) {
			ctx := context.Background()
			configMap := auditLoggingConfigMap(globalEnabled)
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, configMap) })

			isvc := auditLoggingInferenceService(serviceName, override, true)
			Expect(admitAuditLoggingInferenceService(admissionv1.Create, nil, isvc)).To(Succeed())
			auditValue, auditPresent := isvc.Annotations[constants.ODHKserveAuditLogging]
			Expect(auditPresent).To(Equal(wantAnnotation != nil))
			if wantAnnotation != nil {
				Expect(auditValue).To(Equal(*wantAnnotation))
			}
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, isvc) })

			deploymentKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(serviceName),
				Namespace: isvc.Namespace,
			}
			deployment := &appsv1.Deployment{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
				g.Expect(auditLoggingArgs(deployment)).To(Equal(expectedAuditLoggingArgs(isvc, auditEnabled)))
			}, timeout, interval).Should(Succeed())
			originalTemplate := deployment.Spec.Template.DeepCopy()

			persisted := &v1beta1.InferenceService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, persisted)).To(Succeed())
			persistedAuditValue, persistedAuditPresent := persisted.Annotations[constants.ODHKserveAuditLogging]
			Expect(persistedAuditPresent).To(Equal(wantAnnotation != nil))
			if wantAnnotation != nil {
				Expect(persistedAuditValue).To(Equal(*wantAnnotation))
			}

			By("changing the global setting and reconciling an unrelated scaling update")
			latestConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      constants.InferenceServiceConfigMapName,
				Namespace: constants.KServeNamespace,
			}, latestConfigMap)).To(Succeed())
			latestConfigMap.Data[v1beta1.OpenShiftConfigName] = auditLoggingOpenShiftConfig(!globalEnabled)
			Expect(k8sClient.Update(ctx, latestConfigMap)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, persisted)).To(Succeed())
			oldIsvc := persisted.DeepCopy()
			persisted.Spec.Predictor.MaxReplicas = 4
			Expect(admitAuditLoggingInferenceService(admissionv1.Update, oldIsvc, persisted)).To(Succeed())
			persistedAuditValue, persistedAuditPresent = persisted.Annotations[constants.ODHKserveAuditLogging]
			Expect(persistedAuditPresent).To(Equal(wantAnnotation != nil))
			if wantAnnotation != nil {
				Expect(persistedAuditValue).To(Equal(*wantAnnotation))
			}
			Expect(k8sClient.Update(ctx, persisted)).To(Succeed())

			Eventually(func(g Gomega) {
				hpa := &autoscalingv2.HorizontalPodAutoscaler{}
				g.Expect(k8sClient.Get(ctx, deploymentKey, hpa)).To(Succeed())
				g.Expect(hpa.Spec.MaxReplicas).To(Equal(int32(4)))
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				updatedDeployment := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, deploymentKey, updatedDeployment)).To(Succeed())
				g.Expect(updatedDeployment.Spec.Template).To(Equal(*originalTemplate))
				g.Expect(auditLoggingArgs(updatedDeployment)).To(Equal(expectedAuditLoggingArgs(isvc, auditEnabled)))
			}, timeout, interval).Should(Succeed())

			finalIsvc := &v1beta1.InferenceService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, finalIsvc)).To(Succeed())
			finalAuditValue, finalAuditPresent := finalIsvc.Annotations[constants.ODHKserveAuditLogging]
			Expect(finalAuditPresent).To(Equal(wantAnnotation != nil))
			if wantAnnotation != nil {
				Expect(finalAuditValue).To(Equal(*wantAnnotation))
			}
		},
		Entry("inherits an enabled global default", "audit-global-on", true, nil, true, ptr.To("true")),
		Entry("inherits a disabled global default", "audit-global-off", false, nil, false, nil),
		Entry("allows an explicit opt-in", "audit-override-on", false, ptr.To("true"), true, ptr.To("true")),
		Entry("allows an explicit opt-out", "audit-override-off", true, ptr.To("false"), false, ptr.To("false")),
	)

	DescribeTable("rejects effective audit logging without authentication",
		func(serviceName string, globalEnabled bool, override *string) {
			ctx := context.Background()
			configMap := auditLoggingConfigMap(globalEnabled)
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, configMap) })

			isvc := auditLoggingInferenceService(serviceName, override, false)
			Expect(admitAuditLoggingInferenceService(admissionv1.Create, nil, isvc)).To(MatchError(ContainSubstring("requires authentication")))
			Expect(isvc.Annotations[constants.ODHKserveAuditLogging]).To(Equal("true"))
		},
		Entry("when inherited from the global setting", "audit-global-no-auth", true, nil),
		Entry("when explicitly requested", "audit-override-no-auth", false, ptr.To("true")),
	)
})

func configureAuditLoggingEnvTestKubeconfig() {
	kubeconfigPath := filepath.Join(GinkgoT().TempDir(), "kubeconfig")
	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"envtest": {
				Server:                   cfg.Host,
				CertificateAuthorityData: cfg.CAData,
				InsecureSkipTLSVerify:    cfg.Insecure,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"envtest": {
				ClientCertificateData: cfg.CertData,
				ClientKeyData:         cfg.KeyData,
				Token:                 cfg.BearerToken,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"envtest": {Cluster: "envtest", AuthInfo: "envtest"},
		},
		CurrentContext: "envtest",
	}
	Expect(clientcmd.WriteToFile(kubeconfig, kubeconfigPath)).To(Succeed())

	previous, wasSet := os.LookupEnv(clientcmd.RecommendedConfigPathEnvVar)
	Expect(os.Setenv(clientcmd.RecommendedConfigPathEnvVar, kubeconfigPath)).To(Succeed())
	DeferCleanup(func() {
		if wasSet {
			_ = os.Setenv(clientcmd.RecommendedConfigPathEnvVar, previous)
			return
		}
		_ = os.Unsetenv(clientcmd.RecommendedConfigPathEnvVar)
	})
}

func auditLoggingConfigMap(enabled bool) *corev1.ConfigMap {
	configMap := createInferenceServiceConfigMap(getRawKubeTestConfigs())
	configMap.Data[v1beta1.OpenShiftConfigName] = auditLoggingOpenShiftConfig(enabled)
	return configMap
}

func auditLoggingOpenShiftConfig(enabled bool) string {
	return fmt.Sprintf(`{"enableAuditLogging":%t}`, enabled)
}

func auditLoggingInferenceService(name string, override *string, authEnabled bool) *v1beta1.InferenceService {
	annotations := getDefaultAnnotations(constants.AutoscalerClassHPA)
	if authEnabled {
		annotations[constants.ODHKserveRawAuth] = "true"
	}
	if override != nil {
		annotations[constants.ODHKserveAuditLogging] = *override
	}

	return &v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "default",
			Annotations: annotations,
		},
		Spec: v1beta1.InferenceServiceSpec{
			Predictor: v1beta1.PredictorSpec{
				ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{
					MinReplicas: ptr.To(int32(1)),
					MaxReplicas: 3,
				},
				PodSpec: v1beta1.PodSpec{
					Containers: []corev1.Container{{
						Name:      constants.InferenceServiceContainerName,
						Image:     "kserve/audit-test:latest",
						Resources: defaultResource,
					}},
				},
			},
		},
	}
}

func admitAuditLoggingInferenceService(operation admissionv1.Operation, oldIsvc, isvc *v1beta1.InferenceService) error {
	var oldObject []byte
	if oldIsvc != nil {
		var err error
		oldObject, err = json.Marshal(oldIsvc)
		if err != nil {
			return fmt.Errorf("marshal old InferenceService: %w", err)
		}
	}

	ctx := admission.NewContextWithRequest(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: operation,
			OldObject: runtime.RawExtension{Raw: oldObject},
		},
	})
	if err := (&v1beta1.InferenceServiceDefaulter{}).Default(ctx, isvc); err != nil {
		return err
	}

	validator := &v1beta1.InferenceServiceValidator{}
	switch operation {
	case admissionv1.Create:
		_, err := validator.ValidateCreate(ctx, isvc)
		return err
	case admissionv1.Update:
		_, err := validator.ValidateUpdate(ctx, oldIsvc, isvc)
		return err
	default:
		return fmt.Errorf("unsupported admission operation %q", operation)
	}
}

func auditLoggingArgs(deployment *appsv1.Deployment) []string {
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != constants.KubeRbacContainerName {
			continue
		}
		result := make([]string, 0, 5)
		for _, arg := range container.Args {
			name, _, _ := strings.Cut(arg, "=")
			if strings.HasPrefix(name, "--audit-") {
				result = append(result, arg)
			}
		}
		return result
	}
	return nil
}

func expectedAuditLoggingArgs(isvc *v1beta1.InferenceService, enabled bool) []string {
	if !enabled {
		return []string{}
	}
	return []string{
		"--audit-log-enabled",
		"--audit-resource-name=" + isvc.Name,
		"--audit-resource-namespace=" + isvc.Namespace,
		"--audit-resource-type=InferenceService",
		"--audit-ai-provider=KServe",
	}
}
