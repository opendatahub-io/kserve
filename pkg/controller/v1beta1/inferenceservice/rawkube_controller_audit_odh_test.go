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
	"testing"

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
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

func TestResolveAuditLoggingPolicy(t *testing.T) {
	tests := []struct {
		name                 string
		annotations          map[string]string
		predictorAnnotations map[string]string
		deploymentMode       constants.DeploymentModeType
		proxyType            string
		observedProfile      constants.AuditLoggingProfile
		reconciliationPaused bool
		wantRequested        constants.AuditLoggingProfile
		wantDesired          constants.AuditLoggingProfile
		wantConditionStatus  corev1.ConditionStatus
		wantReason           string
		wantMessageParts     []string
	}{
		{
			name:                "legacy annotationless service remains disabled",
			deploymentMode:      constants.Standard,
			wantRequested:       constants.AuditLoggingProfileNone,
			wantDesired:         constants.AuditLoggingProfileNone,
			wantConditionStatus: corev1.ConditionTrue,
			wantReason:          "AuditLoggingDisabled",
		},
		{
			name: "metadata without authentication is disabled",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
			},
			deploymentMode:      constants.Standard,
			wantRequested:       constants.AuditLoggingProfileMetadata,
			wantDesired:         constants.AuditLoggingProfileNone,
			wantConditionStatus: corev1.ConditionFalse,
			wantReason:          "AuthenticationRequired",
			wantMessageParts:    []string{constants.ODHKserveRawAuth},
		},
		{
			name: "disabling authentication deactivates observed metadata",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
			},
			deploymentMode:      constants.Standard,
			proxyType:           constants.KubeRbacContainerName,
			observedProfile:     constants.AuditLoggingProfileMetadata,
			wantRequested:       constants.AuditLoggingProfileMetadata,
			wantDesired:         constants.AuditLoggingProfileNone,
			wantConditionStatus: corev1.ConditionFalse,
			wantReason:          "AuthenticationRequired",
			wantMessageParts:    []string{constants.ODHKserveRawAuth},
		},
		{
			name:                "removed annotation remains reconciling while metadata is observed",
			deploymentMode:      constants.Standard,
			proxyType:           constants.KubeRbacContainerName,
			observedProfile:     constants.AuditLoggingProfileMetadata,
			wantRequested:       constants.AuditLoggingProfileNone,
			wantDesired:         constants.AuditLoggingProfileNone,
			wantConditionStatus: corev1.ConditionFalse,
			wantReason:          "AuditLoggingReconciling",
		},
		{
			name: "metadata with authentication and kube rbac proxy is enabled",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
				constants.ODHKserveRawAuth:             "true",
			},
			deploymentMode:      constants.Standard,
			proxyType:           constants.KubeRbacContainerName,
			observedProfile:     constants.AuditLoggingProfileMetadata,
			wantRequested:       constants.AuditLoggingProfileMetadata,
			wantDesired:         constants.AuditLoggingProfileMetadata,
			wantConditionStatus: corev1.ConditionTrue,
			wantReason:          "AuditLoggingEnabled",
		},
		{
			name: "metadata remains reconciling until the deployment observes it",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
				constants.ODHKserveRawAuth:             "true",
			},
			deploymentMode:      constants.Standard,
			proxyType:           constants.KubeRbacContainerName,
			wantRequested:       constants.AuditLoggingProfileMetadata,
			wantDesired:         constants.AuditLoggingProfileMetadata,
			wantConditionStatus: corev1.ConditionFalse,
			wantReason:          "AuditLoggingReconciling",
		},
		{
			name: "metadata outside standard mode is disabled",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
				constants.ODHKserveRawAuth:             "true",
			},
			deploymentMode:      constants.Knative,
			wantRequested:       constants.AuditLoggingProfileMetadata,
			wantDesired:         constants.AuditLoggingProfileNone,
			wantConditionStatus: corev1.ConditionFalse,
			wantReason:          "UnsupportedDeploymentMode",
			wantMessageParts:    []string{string(constants.Knative)},
		},
		{
			name:                "global metadata ignored outside standard mode remains none",
			deploymentMode:      constants.Knative,
			wantRequested:       constants.AuditLoggingProfileNone,
			wantDesired:         constants.AuditLoggingProfileNone,
			wantConditionStatus: corev1.ConditionTrue,
			wantReason:          "AuditLoggingDisabled",
		},
		{
			name:                 "component annotation is ignored with warning",
			predictorAnnotations: map[string]string{constants.ODHKserveAuditLoggingProfile: "metadata"},
			deploymentMode:       constants.Standard,
			wantRequested:        constants.AuditLoggingProfileNone,
			wantDesired:          constants.AuditLoggingProfileNone,
			wantConditionStatus:  corev1.ConditionFalse,
			wantReason:           "ComponentAnnotationIgnored",
			wantMessageParts:     []string{"predictor"},
		},
		{
			name: "legacy oauth proxy requires explicit migration",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
				constants.ODHKserveRawAuth:             "true",
			},
			deploymentMode:      constants.Standard,
			proxyType:           constants.OauthProxyContainerName,
			wantRequested:       constants.AuditLoggingProfileMetadata,
			wantDesired:         constants.AuditLoggingProfileNone,
			wantConditionStatus: corev1.ConditionFalse,
			wantReason:          "ProxyMigrationRequired",
			wantMessageParts:    []string{constants.ODHAuthProxyTypeAnnotation},
		},
		{
			name: "explicit migration allows metadata",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
				constants.ODHKserveRawAuth:             "true",
				constants.ODHAuthProxyTypeAnnotation:   constants.KubeRbacProxyType,
			},
			deploymentMode:      constants.Standard,
			proxyType:           constants.OauthProxyContainerName,
			wantRequested:       constants.AuditLoggingProfileMetadata,
			wantDesired:         constants.AuditLoggingProfileMetadata,
			wantConditionStatus: corev1.ConditionFalse,
			wantReason:          "AuditLoggingReconciling",
		},
		{
			name: "paused reconciliation preserves observed profile",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
				constants.ODHKserveRawAuth:             "true",
			},
			deploymentMode:       constants.Standard,
			proxyType:            constants.KubeRbacContainerName,
			observedProfile:      constants.AuditLoggingProfileNone,
			reconciliationPaused: true,
			wantRequested:        constants.AuditLoggingProfileMetadata,
			wantDesired:          constants.AuditLoggingProfileMetadata,
			wantConditionStatus:  corev1.ConditionFalse,
			wantReason:           "ReconciliationDisabled",
		},
		{
			name: "multiple configuration issues are combined deterministically",
			annotations: map[string]string{
				constants.ODHKserveAuditLoggingProfile: "metadata",
			},
			predictorAnnotations: map[string]string{constants.ODHKserveAuditLoggingProfile: "none"},
			deploymentMode:       constants.Knative,
			wantRequested:        constants.AuditLoggingProfileMetadata,
			wantDesired:          constants.AuditLoggingProfileNone,
			wantConditionStatus:  corev1.ConditionFalse,
			wantReason:           "MultipleConfigurationIssues",
			wantMessageParts:     []string{"predictor", string(constants.Knative), constants.ODHKserveRawAuth},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isvc := &v1beta1.InferenceService{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Spec: v1beta1.InferenceServiceSpec{Predictor: v1beta1.PredictorSpec{
					ComponentExtensionSpec: v1beta1.ComponentExtensionSpec{Annotations: tt.predictorAnnotations},
				}},
			}

			got := resolveAuditLoggingPolicy(isvc, tt.deploymentMode, tt.proxyType, tt.observedProfile, tt.reconciliationPaused)
			if got.requestedProfile != tt.wantRequested {
				t.Fatalf("requested profile = %q, want %q", got.requestedProfile, tt.wantRequested)
			}
			if got.desiredProfile != tt.wantDesired {
				t.Fatalf("desired profile = %q, want %q", got.desiredProfile, tt.wantDesired)
			}
			if got.condition.Status != tt.wantConditionStatus || got.condition.Reason != tt.wantReason {
				t.Fatalf("condition = %#v, want status %q reason %q", got.condition, tt.wantConditionStatus, tt.wantReason)
			}
			previousIndex := -1
			for _, part := range tt.wantMessageParts {
				index := strings.Index(got.condition.Message, part)
				if index < 0 {
					t.Fatalf("condition message = %q, want substring %q", got.condition.Message, part)
				}
				if index <= previousIndex {
					t.Fatalf("condition message = %q, want %q after prior message part", got.condition.Message, part)
				}
				previousIndex = index
			}
		})
	}
}

func TestApplyAuditLoggingResolutionEmitsWarningOnlyOnTransition(t *testing.T) {
	isvc := &v1beta1.InferenceService{}
	recorder := record.NewFakeRecorder(2)
	resolution := resolveAuditLoggingPolicy(&v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			constants.ODHKserveAuditLoggingProfile: "metadata",
		}},
	}, constants.Standard, "", constants.AuditLoggingProfileNone, false)

	applyAuditLoggingResolution(isvc, resolution, recorder)
	condition := isvc.Status.GetCondition(auditLoggingConfiguredCondition)
	if condition == nil || condition.Status != corev1.ConditionFalse || condition.Reason != "AuthenticationRequired" {
		t.Fatalf("audit condition = %#v, want false AuthenticationRequired", condition)
	}
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "AuthenticationRequired") {
			t.Fatalf("event = %q, want AuthenticationRequired", event)
		}
	default:
		t.Fatal("expected warning event for new audit logging issue")
	}

	applyAuditLoggingResolution(isvc, resolution, recorder)
	select {
	case event := <-recorder.Events:
		t.Fatalf("unexpected duplicate event = %q", event)
	default:
	}

	changedResolution := resolveAuditLoggingPolicy(&v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			constants.ODHKserveAuditLoggingProfile: "metadata",
		}},
	}, constants.Knative, "", constants.AuditLoggingProfileNone, false)
	applyAuditLoggingResolution(isvc, changedResolution, recorder)
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "MultipleConfigurationIssues") {
			t.Fatalf("event = %q, want changed advisory condition", event)
		}
	default:
		t.Fatal("expected warning event when the advisory condition changes")
	}
}

func TestAuditLoggingConditionDoesNotAffectReadiness(t *testing.T) {
	isvc := &v1beta1.InferenceService{}
	isvc.Status.InitializeConditions()
	isvc.Status.SetCondition(v1beta1.PredictorReady, &apis.Condition{Status: corev1.ConditionTrue})
	isvc.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{Status: corev1.ConditionTrue})
	isvc.Status.SetCondition(auditLoggingConfiguredCondition, &apis.Condition{
		Status:  corev1.ConditionFalse,
		Reason:  "AuthenticationRequired",
		Message: "Enable authentication to activate metadata audit logging.",
	})

	if !isvc.Status.IsReady() {
		t.Fatal("advisory audit condition must not affect InferenceService readiness")
	}
}

var _ = Describe("RawDeployment audit logging", func() {
	BeforeEach(func() {
		configureAuditLoggingEnvTestKubeconfig()
	})

	DescribeTable("renders the effective audit profile and keeps it stable across global changes",
		func(serviceName string, globalProfile constants.AuditLoggingProfile, override *string, effectiveProfile constants.AuditLoggingProfile, wantAnnotation *string) {
			ctx := context.Background()
			configMap := auditLoggingConfigMap(globalProfile)
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, configMap) })

			isvc := auditLoggingInferenceService(serviceName, override, true)
			Expect(admitAuditLoggingInferenceService(admissionv1.Create, nil, isvc)).To(Succeed())
			auditValue, auditPresent := isvc.Annotations[constants.ODHKserveAuditLoggingProfile]
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
				g.Expect(auditLoggingArgs(deployment)).To(Equal(expectedAuditLoggingArgs(isvc, effectiveProfile)))
			}, timeout, interval).Should(Succeed())
			originalTemplate := deployment.Spec.Template.DeepCopy()

			Eventually(func(g Gomega) {
				current := &v1beta1.InferenceService{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, current)).To(Succeed())
				g.Expect(current.Status.DeploymentMode).To(Equal(string(constants.Standard)))
				condition := current.Status.GetCondition(auditLoggingConfiguredCondition)
				g.Expect(condition != nil).To(Equal(wantAnnotation != nil))
			}, timeout, interval).Should(Succeed())

			persisted := &v1beta1.InferenceService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, persisted)).To(Succeed())
			persistedAuditValue, persistedAuditPresent := persisted.Annotations[constants.ODHKserveAuditLoggingProfile]
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
			changedGlobalProfile := constants.AuditLoggingProfileMetadata
			if globalProfile == constants.AuditLoggingProfileMetadata {
				changedGlobalProfile = constants.AuditLoggingProfileNone
			}
			latestConfigMap.Data[v1beta1.OpenShiftConfigName] = auditLoggingOpenShiftConfig(changedGlobalProfile)
			Expect(k8sClient.Update(ctx, latestConfigMap)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, persisted)).To(Succeed())
			oldIsvc := persisted.DeepCopy()
			persisted.Spec.Predictor.MaxReplicas = 4
			Expect(admitAuditLoggingInferenceService(admissionv1.Update, oldIsvc, persisted)).To(Succeed())
			persistedAuditValue, persistedAuditPresent = persisted.Annotations[constants.ODHKserveAuditLoggingProfile]
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
				g.Expect(auditLoggingArgs(updatedDeployment)).To(Equal(expectedAuditLoggingArgs(isvc, effectiveProfile)))
			}, timeout, interval).Should(Succeed())

			finalIsvc := &v1beta1.InferenceService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, finalIsvc)).To(Succeed())
			finalAuditValue, finalAuditPresent := finalIsvc.Annotations[constants.ODHKserveAuditLoggingProfile]
			Expect(finalAuditPresent).To(Equal(wantAnnotation != nil))
			if wantAnnotation != nil {
				Expect(finalAuditValue).To(Equal(*wantAnnotation))
			}
		},
		Entry("inherits metadata", "audit-global-metadata", constants.AuditLoggingProfileMetadata, nil, constants.AuditLoggingProfileMetadata, ptr.To("metadata")),
		Entry("inherits none without annotation", "audit-global-none", constants.AuditLoggingProfileNone, nil, constants.AuditLoggingProfileNone, nil),
		Entry("allows explicit metadata", "audit-override-metadata", constants.AuditLoggingProfileNone, ptr.To("metadata"), constants.AuditLoggingProfileMetadata, ptr.To("metadata")),
		Entry("allows explicit none", "audit-override-none", constants.AuditLoggingProfileMetadata, ptr.To("none"), constants.AuditLoggingProfileNone, ptr.To("none")),
	)

	DescribeTable("records requested metadata while keeping audit disabled without authentication",
		func(serviceName string, globalProfile constants.AuditLoggingProfile, override *string, wantConditionStatus corev1.ConditionStatus, wantReason string) {
			ctx := context.Background()
			configMap := auditLoggingConfigMap(globalProfile)
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, configMap) })

			isvc := auditLoggingInferenceService(serviceName, override, false)
			Expect(admitAuditLoggingInferenceService(admissionv1.Create, nil, isvc)).To(Succeed())
			Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, isvc) })

			deploymentKey := types.NamespacedName{
				Name:      constants.PredictorServiceName(serviceName),
				Namespace: isvc.Namespace,
			}
			Eventually(func(g Gomega) {
				deployment := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
				g.Expect(auditLoggingArgs(deployment)).To(BeEmpty())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				persisted := &v1beta1.InferenceService{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, persisted)).To(Succeed())
				expectAuditLoggingCondition(g, persisted, wantConditionStatus, wantReason)
			}, timeout, interval).Should(Succeed())
		},
		Entry("inherits metadata", "audit-global-no-auth", constants.AuditLoggingProfileMetadata, nil, corev1.ConditionFalse, "AuthenticationRequired"),
		Entry("accepts explicit metadata", "audit-override-no-auth", constants.AuditLoggingProfileNone, ptr.To("metadata"), corev1.ConditionFalse, "AuthenticationRequired"),
	)

	It("activates a retained request and removes managed arguments after explicit removal", func() {
		ctx := context.Background()
		configMap := auditLoggingConfigMap(constants.AuditLoggingProfileMetadata)
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, configMap) })

		isvc := auditLoggingInferenceService("audit-auth-transition", nil, false)
		Expect(admitAuditLoggingInferenceService(admissionv1.Create, nil, isvc)).To(Succeed())
		Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, isvc) })

		isvcKey := types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}
		deploymentKey := types.NamespacedName{Name: constants.PredictorServiceName(isvc.Name), Namespace: isvc.Namespace}
		Eventually(func(g Gomega) {
			persisted := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, isvcKey, persisted)).To(Succeed())
			expectAuditLoggingCondition(g, persisted, corev1.ConditionFalse, "AuthenticationRequired")
			deployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			g.Expect(auditLoggingArgs(deployment)).To(BeEmpty())
		}, timeout, interval).Should(Succeed())

		By("enabling authentication without changing the retained metadata request")
		persisted := &v1beta1.InferenceService{}
		Expect(k8sClient.Get(ctx, isvcKey, persisted)).To(Succeed())
		oldIsvc := persisted.DeepCopy()
		persisted.Annotations[constants.ODHKserveRawAuth] = "true"
		Expect(admitAuditLoggingInferenceService(admissionv1.Update, oldIsvc, persisted)).To(Succeed())
		Expect(k8sClient.Update(ctx, persisted)).To(Succeed())
		Eventually(func(g Gomega) {
			current := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, isvcKey, current)).To(Succeed())
			expectAuditLoggingCondition(g, current, corev1.ConditionTrue, "AuditLoggingEnabled")
			deployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			g.Expect(auditLoggingArgs(deployment)).To(Equal(expectedAuditLoggingArgs(isvc, constants.AuditLoggingProfileMetadata)))
		}, timeout, interval).Should(Succeed())

		By("disabling authentication while retaining the request")
		Expect(k8sClient.Get(ctx, isvcKey, persisted)).To(Succeed())
		oldIsvc = persisted.DeepCopy()
		persisted.Annotations[constants.ODHKserveRawAuth] = "false"
		Expect(admitAuditLoggingInferenceService(admissionv1.Update, oldIsvc, persisted)).To(Succeed())
		Expect(k8sClient.Update(ctx, persisted)).To(Succeed())
		Eventually(func(g Gomega) {
			current := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, isvcKey, current)).To(Succeed())
			expectAuditLoggingCondition(g, current, corev1.ConditionFalse, "AuthenticationRequired")
			deployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			g.Expect(auditLoggingArgs(deployment)).To(BeEmpty())
		}, timeout, interval).Should(Succeed())

		By("re-enabling authentication and then explicitly removing the audit annotation")
		Expect(k8sClient.Get(ctx, isvcKey, persisted)).To(Succeed())
		oldIsvc = persisted.DeepCopy()
		persisted.Annotations[constants.ODHKserveRawAuth] = "true"
		Expect(admitAuditLoggingInferenceService(admissionv1.Update, oldIsvc, persisted)).To(Succeed())
		Expect(k8sClient.Update(ctx, persisted)).To(Succeed())
		Eventually(func(g Gomega) {
			current := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, isvcKey, current)).To(Succeed())
			expectAuditLoggingCondition(g, current, corev1.ConditionTrue, "AuditLoggingEnabled")
		}, timeout, interval).Should(Succeed())

		Expect(k8sClient.Get(ctx, isvcKey, persisted)).To(Succeed())
		oldIsvc = persisted.DeepCopy()
		delete(persisted.Annotations, constants.ODHKserveAuditLoggingProfile)
		Expect(admitAuditLoggingInferenceService(admissionv1.Update, oldIsvc, persisted)).To(Succeed())
		_, annotationPresent := persisted.Annotations[constants.ODHKserveAuditLoggingProfile]
		Expect(annotationPresent).To(BeFalse())
		Expect(k8sClient.Update(ctx, persisted)).To(Succeed())
		Eventually(func(g Gomega) {
			current := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, isvcKey, current)).To(Succeed())
			expectAuditLoggingCondition(g, current, corev1.ConditionTrue, "AuditLoggingDisabled")
			deployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			g.Expect(auditLoggingArgs(deployment)).To(BeEmpty())
		}, timeout, interval).Should(Succeed())
	})

	It("filters component audit annotations while honoring the top-level policy", func() {
		ctx := context.Background()
		configMap := auditLoggingConfigMap(constants.AuditLoggingProfileNone)
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, configMap) })

		isvc := auditLoggingInferenceService("audit-component-annotation", ptr.To("metadata"), true)
		isvc.Spec.Predictor.Annotations = map[string]string{constants.ODHKserveAuditLoggingProfile: "none"}
		Expect(admitAuditLoggingInferenceService(admissionv1.Create, nil, isvc)).To(Succeed())
		Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, isvc) })

		isvcKey := types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}
		deploymentKey := types.NamespacedName{Name: constants.PredictorServiceName(isvc.Name), Namespace: isvc.Namespace}
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			_, deploymentAnnotation := deployment.Annotations[constants.ODHKserveAuditLoggingProfile]
			_, templateAnnotation := deployment.Spec.Template.Annotations[constants.ODHKserveAuditLoggingProfile]
			g.Expect(deploymentAnnotation).To(BeFalse())
			g.Expect(templateAnnotation).To(BeFalse())
			g.Expect(auditLoggingArgs(deployment)).To(Equal(expectedAuditLoggingArgs(isvc, constants.AuditLoggingProfileMetadata)))

			persisted := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, isvcKey, persisted)).To(Succeed())
			expectAuditLoggingCondition(g, persisted, corev1.ConditionFalse, "ComponentAnnotationIgnored")
		}, timeout, interval).Should(Succeed())
	})

	It("does not claim audit status ownership for ModelMesh", func() {
		ctx := context.Background()
		configMap := auditLoggingConfigMap(constants.AuditLoggingProfileNone)
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, configMap) })

		isvc := auditLoggingInferenceService("audit-modelmesh-status", ptr.To("metadata"), true)
		isvc.Annotations[constants.DeploymentMode] = string(constants.ModelMeshDeployment)
		Expect(admitAuditLoggingInferenceService(admissionv1.Create, nil, isvc)).To(Succeed())
		Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, isvc) })

		Eventually(func(g Gomega) {
			persisted := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}, persisted)).To(Succeed())
			g.Expect(persisted.Status.DeploymentMode).To(Equal(string(constants.ModelMeshDeployment)))
			g.Expect(persisted.Status.GetCondition(auditLoggingConfiguredCondition)).To(BeNil())
		}, timeout, interval).Should(Succeed())
	})

	It("records a pending transition without changing workloads when reconciliation is disabled", func() {
		ctx := context.Background()
		configMap := auditLoggingConfigMap(constants.AuditLoggingProfileNone)
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, configMap) })

		isvc := auditLoggingInferenceService("audit-reconciliation-disabled", ptr.To("metadata"), true)
		Expect(admitAuditLoggingInferenceService(admissionv1.Create, nil, isvc)).To(Succeed())
		Expect(k8sClient.Create(ctx, isvc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, isvc) })

		isvcKey := types.NamespacedName{Name: isvc.Name, Namespace: isvc.Namespace}
		deploymentKey := types.NamespacedName{Name: constants.PredictorServiceName(isvc.Name), Namespace: isvc.Namespace}
		deployment := &appsv1.Deployment{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			g.Expect(auditLoggingArgs(deployment)).To(Equal(expectedAuditLoggingArgs(isvc, constants.AuditLoggingProfileMetadata)))
		}, timeout, interval).Should(Succeed())
		originalTemplate := deployment.Spec.Template.DeepCopy()

		persisted := &v1beta1.InferenceService{}
		Expect(k8sClient.Get(ctx, isvcKey, persisted)).To(Succeed())
		persisted.Status.SetCondition(v1beta1.PredictorReady, &apis.Condition{Type: v1beta1.PredictorReady, Status: corev1.ConditionTrue})
		persisted.Status.SetCondition(v1beta1.IngressReady, &apis.Condition{Type: v1beta1.IngressReady, Status: corev1.ConditionTrue})
		Expect(k8sClient.Status().Update(ctx, persisted)).To(Succeed())
		Eventually(func(g Gomega) {
			current := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, isvcKey, current)).To(Succeed())
			g.Expect(current.Status.IsReady()).To(BeTrue())
		}, timeout, interval).Should(Succeed())

		Expect(k8sClient.Get(ctx, isvcKey, persisted)).To(Succeed())
		persisted.Annotations[constants.DisableAutoUpdateAnnotationKey] = "true"
		persisted.Annotations[constants.ODHKserveRawAuth] = "false"
		Expect(k8sClient.Update(ctx, persisted)).To(Succeed())

		Eventually(func(g Gomega) {
			current := &v1beta1.InferenceService{}
			g.Expect(k8sClient.Get(ctx, isvcKey, current)).To(Succeed())
			g.Expect(current.Status.IsReady()).To(BeTrue())
			condition := expectAuditLoggingCondition(g, current, corev1.ConditionFalse, "MultipleConfigurationIssues")
			if condition != nil {
				g.Expect(condition.Message).To(ContainSubstring("automatic reconciliation is disabled"))
			}

			updatedDeployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, deploymentKey, updatedDeployment)).To(Succeed())
			g.Expect(updatedDeployment.Spec.Template).To(Equal(*originalTemplate))
		}, timeout, interval).Should(Succeed())
	})
})

func expectAuditLoggingCondition(g Gomega, isvc *v1beta1.InferenceService, status corev1.ConditionStatus, reason string) *apis.Condition {
	condition := isvc.Status.GetCondition(auditLoggingConfiguredCondition)
	g.Expect(condition).ToNot(BeNil())
	if condition == nil {
		return nil
	}
	g.Expect(condition.Status).To(Equal(status))
	g.Expect(condition.Reason).To(Equal(reason))
	return condition
}

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

func auditLoggingConfigMap(profile constants.AuditLoggingProfile) *corev1.ConfigMap {
	configMap := createInferenceServiceConfigMap(getRawKubeTestConfigs())
	configMap.Data[v1beta1.OpenShiftConfigName] = auditLoggingOpenShiftConfig(profile)
	return configMap
}

func auditLoggingOpenShiftConfig(profile constants.AuditLoggingProfile) string {
	return fmt.Sprintf(`{"auditLoggingProfile":%q}`, profile)
}

func auditLoggingInferenceService(name string, override *string, authEnabled bool) *v1beta1.InferenceService {
	annotations := getDefaultAnnotations(constants.AutoscalerClassHPA)
	if authEnabled {
		annotations[constants.ODHKserveRawAuth] = "true"
	}
	if override != nil {
		annotations[constants.ODHKserveAuditLoggingProfile] = *override
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

func expectedAuditLoggingArgs(isvc *v1beta1.InferenceService, profile constants.AuditLoggingProfile) []string {
	switch profile {
	case constants.AuditLoggingProfileNone:
		return []string{}
	case constants.AuditLoggingProfileMetadata:
		return []string{
			"--audit-log-profile=metadata",
			"--audit-resource-name=" + isvc.Name,
			"--audit-resource-namespace=" + isvc.Namespace,
			"--audit-resource-type=InferenceService",
			"--audit-ai-provider=KServe",
		}
	default:
		return nil
	}
}
