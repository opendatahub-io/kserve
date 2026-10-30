//go:build distro

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

package llmisvc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"knative.dev/pkg/kmeta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"

	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
	. "github.com/kserve/kserve/pkg/testing"
)

var _ = Describe("LLMInferenceService OCP SCC Controller", func() {
	Context("Multi-Node SCC RoleBinding Reconciliation", func() {
		It("should create SCC RoleBinding when worker spec is set", func(ctx SpecContext) {
			// given
			svcName := "test-scc-worker"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(1),
				)),
				WithWorker(&corev1.PodSpec{}),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then - SCC RoleBinding should be created
			sccRB := &rbacv1.RoleBinding{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve-mn-scc"),
					Namespace: testNs.Name,
				}, sccRB)
			}).WithContext(ctx).Should(Succeed())

			Expect(sccRB).To(BeOwnedBy(llmSvc))
			Expect(sccRB.RoleRef.Kind).To(Equal("ClusterRole"))
			Expect(sccRB.RoleRef.Name).To(Equal("openshift-ai-llminferenceservice-scc"))
			Expect(sccRB.RoleRef.APIGroup).To(Equal(rbacv1.GroupName))

			// Verify subjects contain both main and prefill service accounts
			Expect(sccRB.Subjects).To(HaveLen(2))
			Expect(sccRB.Subjects[0].Kind).To(Equal("ServiceAccount"))
			Expect(sccRB.Subjects[0].Name).To(Equal(kmeta.ChildName(svcName, "-kserve-mn")))
			Expect(sccRB.Subjects[1].Kind).To(Equal("ServiceAccount"))
			Expect(sccRB.Subjects[1].Name).To(Equal(kmeta.ChildName(svcName, "-kserve-mn-prefill")))
		})

		It("should create SCC RoleBinding when prefill worker spec is set", func(ctx SpecContext) {
			// given
			svcName := "test-scc-prefill"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithPrefillParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(1),
				)),
				WithPrefillWorker(&corev1.PodSpec{}),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then - SCC RoleBinding should be created
			sccRB := &rbacv1.RoleBinding{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve-mn-scc"),
					Namespace: testNs.Name,
				}, sccRB)
			}).WithContext(ctx).Should(Succeed())

			Expect(sccRB).To(BeOwnedBy(llmSvc))
			Expect(sccRB.RoleRef.Name).To(Equal("openshift-ai-llminferenceservice-scc"))
		})

		It("should create SCC RoleBinding with both worker and prefill specs", func(ctx SpecContext) {
			// given
			svcName := "test-scc-both"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(1),
				)),
				WithWorker(SimpleWorkerPodSpec()),
				WithPrefillParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(1),
				)),
				WithPrefillWorker(SimpleWorkerPodSpec()),
				WithPrefillReplicas(1),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then - SCC RoleBinding should be created
			sccRB := &rbacv1.RoleBinding{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve-mn-scc"),
					Namespace: testNs.Name,
				}, sccRB)
			}).WithContext(ctx).Should(Succeed())

			Expect(sccRB).To(BeOwnedBy(llmSvc))
			Expect(sccRB.Subjects).To(HaveLen(2))
			Expect(sccRB.Subjects[0].Name).To(Equal(kmeta.ChildName(svcName, "-kserve-mn")))
			Expect(sccRB.Subjects[1].Name).To(Equal(kmeta.ChildName(svcName, "-kserve-mn-prefill")))
		})

		It("should delete SCC RoleBinding when worker and prefill specs are removed", func(ctx SpecContext) {
			// given
			svcName := "test-scc-cleanup"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(1),
				)),
				WithWorker(&corev1.PodSpec{}),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			sccRBName := types.NamespacedName{
				Name:      kmeta.ChildName(svcName, "-kserve-mn-scc"),
				Namespace: testNs.Name,
			}

			// Verify SCC RoleBinding is created
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, sccRBName, &rbacv1.RoleBinding{})
			}).WithContext(ctx).Should(Succeed())

			// when - Remove worker spec
			errRetry := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				_, errUpdate := ctrl.CreateOrUpdate(ctx, envTest.Client, llmSvc, func() error {
					llmSvc.Spec.Worker = nil
					return nil
				})
				return errUpdate
			})
			Expect(errRetry).ToNot(HaveOccurred())

			// then - SCC RoleBinding should be deleted
			Eventually(func(g Gomega, ctx context.Context) bool {
				err := envTest.Get(ctx, sccRBName, &rbacv1.RoleBinding{})
				return apierrors.IsNotFound(err)
			}).WithContext(ctx).Should(BeTrue())
		})

		It("should delete SCC RoleBinding when stop annotation is set", func(ctx SpecContext) {
			// given
			svcName := "test-scc-stop"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(1),
				)),
				WithWorker(&corev1.PodSpec{}),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			sccRBName := types.NamespacedName{
				Name:      kmeta.ChildName(svcName, "-kserve-mn-scc"),
				Namespace: testNs.Name,
			}

			// Verify SCC RoleBinding is created
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, sccRBName, &rbacv1.RoleBinding{})
			}).WithContext(ctx).Should(Succeed())

			// when - Set the stop annotation
			errRetry := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				_, errUpdate := ctrl.CreateOrUpdate(ctx, envTest.Client, llmSvc, func() error {
					if llmSvc.Annotations == nil {
						llmSvc.Annotations = make(map[string]string)
					}
					llmSvc.Annotations[constants.StopAnnotationKey] = "true"
					return nil
				})
				return errUpdate
			})
			Expect(errRetry).ToNot(HaveOccurred())

			// then - SCC RoleBinding should be deleted
			Eventually(func(g Gomega, ctx context.Context) bool {
				err := envTest.Get(ctx, sccRBName, &rbacv1.RoleBinding{})
				return apierrors.IsNotFound(err)
			}).WithContext(ctx).Should(BeTrue())
		})

		It("should have correct labels on SCC RoleBinding", func(ctx SpecContext) {
			// given
			svcName := "test-scc-labels"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithParallelism(ParallelismSpec(
					WithDataParallelism(2),
					WithDataLocalParallelism(1),
				)),
				WithWorker(&corev1.PodSpec{}),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			sccRB := &rbacv1.RoleBinding{}
			Eventually(func(g Gomega, ctx context.Context) error {
				return envTest.Get(ctx, types.NamespacedName{
					Name:      kmeta.ChildName(svcName, "-kserve-mn-scc"),
					Namespace: testNs.Name,
				}, sccRB)
			}).WithContext(ctx).Should(Succeed())

			Expect(sccRB.Labels).To(HaveKeyWithValue(constants.KubernetesAppNameLabelKey, svcName))
			Expect(sccRB.Labels).To(HaveKeyWithValue(constants.KubernetesPartOfLabelKey, constants.LLMInferenceServicePartOfValue))
		})
	})

	Context("Single-Node SCC RoleBinding Reconciliation", func() {
		BeforeEach(func(ctx SpecContext) {
			Expect(client.IgnoreAlreadyExists(envTest.Create(ctx, shippedSCCClusterRole()))).To(Succeed())
		})

		It("should grant the SCC to the ModelExpress ServiceAccount of a pod that adds IPC_LOCK", func(ctx SpecContext) {
			// given
			svcName := "test-scc-sn-mx"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://Qwen/Qwen3-8B"),
				WithAnnotations(map[string]string{
					constants.ModelExpressModeAnnotationKey:    "native",
					constants.ModelExpressAddressAnnotationKey: "https://modelexpress.mx.svc:8001",
				}),
				WithTemplate(podAddingCapabilities("IPC_LOCK")),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			serviceAccountName := kmeta.ChildName(svcName, "-kserve")
			Expect(deploymentServiceAccountName(ctx, serviceAccountName, testNs.Name)).To(Equal(serviceAccountName))

			sccRB := singleNodeSCCRoleBinding(ctx, svcName, testNs.Name)
			Expect(sccRB).To(BeOwnedBy(llmSvc))
			Expect(sccRB.Labels).To(HaveKeyWithValue(constants.KubernetesAppNameLabelKey, svcName))
			Expect(sccRB.Labels).To(HaveKeyWithValue(constants.KubernetesPartOfLabelKey, constants.LLMInferenceServicePartOfValue))
			Expect(sccRB.RoleRef).To(Equal(rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "openshift-ai-llminferenceservice-scc",
			}))
			Expect(sccRB.Subjects).To(Equal([]rbacv1.Subject{
				{Kind: rbacv1.ServiceAccountKind, Name: serviceAccountName, Namespace: testNs.Name},
			}))

			expectSCCAccess(ctx, testNs.Name, serviceAccountName, true)
			expectSCCAccess(ctx, testNs.Name, "default", false)
		})

		It("should not grant the SCC when no container adds a capability restricted-v2 rejects", func(ctx SpecContext) {
			// given
			svcName := "test-scc-sn-restricted"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(podAddingCapabilities("NET_BIND_SERVICE")),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			Expect(deploymentServiceAccountName(ctx, kmeta.ChildName(svcName, "-kserve"), testNs.Name)).To(BeEmpty())
			Consistently(singleNodeSCCRoleBindingGone(svcName, testNs.Name)).WithContext(ctx).Should(BeTrue())
			expectSCCAccess(ctx, testNs.Name, "default", false)
		})

		It("should grant the SCC to the default ServiceAccount a pod runs as when it names none", func(ctx SpecContext) {
			// given
			svcName := "test-scc-sn-default-sa"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(podAddingCapabilities("IPC_LOCK", "NET_RAW")),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			Expect(deploymentServiceAccountName(ctx, kmeta.ChildName(svcName, "-kserve"), testNs.Name)).To(BeEmpty())
			sccRB := singleNodeSCCRoleBinding(ctx, svcName, testNs.Name)
			Expect(sccRB.Subjects).To(Equal([]rbacv1.Subject{
				{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: testNs.Name},
			}))
			expectSCCAccess(ctx, testNs.Name, "default", true)
		})

		It("should grant the SCC to the ServiceAccount the template names when an init container adds a capability", func(ctx SpecContext) {
			// given
			svcName := "test-scc-sn-custom-sa"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName), WithServiceAccount("rdma-engine"))

			pod := SimpleWorkerPodSpec()
			pod.ServiceAccountName = "rdma-engine"
			pod.InitContainers = []corev1.Container{{
				Name:            "rdma-probe",
				Image:           "test-probe:latest",
				SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_RAWIO"}}},
			}}
			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(pod),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			Expect(deploymentServiceAccountName(ctx, kmeta.ChildName(svcName, "-kserve"), testNs.Name)).To(Equal("rdma-engine"))
			sccRB := singleNodeSCCRoleBinding(ctx, svcName, testNs.Name)
			Expect(sccRB.Subjects).To(Equal([]rbacv1.Subject{
				{Kind: rbacv1.ServiceAccountKind, Name: "rdma-engine", Namespace: testNs.Name},
			}))
			expectSCCAccess(ctx, testNs.Name, "rdma-engine", true)
			expectSCCAccess(ctx, testNs.Name, "default", false)
		})

		It("should follow the P/D pods that add capabilities and revoke the SCC when they stop", func(ctx SpecContext) {
			// given
			svcName := "test-scc-sn-pd"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(podAddingCapabilities("IPC_LOCK")),
				WithPrefill(podAddingCapabilities("IPC_LOCK")),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			mainServiceAccount := kmeta.ChildName(svcName, "-kserve")
			Expect(deploymentServiceAccountName(ctx, mainServiceAccount, testNs.Name)).To(Equal(mainServiceAccount))
			Expect(deploymentServiceAccountName(ctx, kmeta.ChildName(svcName, "-kserve-prefill"), testNs.Name)).To(BeEmpty())
			Eventually(func(g Gomega, ctx context.Context) {
				sccRB := &rbacv1.RoleBinding{}
				g.Expect(envTest.Get(ctx, singleNodeSCCRoleBindingName(svcName, testNs.Name), sccRB)).To(Succeed())
				g.Expect(sccRB.Subjects).To(Equal([]rbacv1.Subject{
					{Kind: rbacv1.ServiceAccountKind, Name: mainServiceAccount, Namespace: testNs.Name},
					{Kind: rbacv1.ServiceAccountKind, Name: "default", Namespace: testNs.Name},
				}))
			}).WithContext(ctx).Should(Succeed())

			// when - prefill stops adding capabilities
			updateLLMInferenceService(ctx, llmSvc, func(svc *v1alpha2.LLMInferenceService) {
				svc.Spec.Prefill.Template = SimpleWorkerPodSpec()
			})

			// then
			Eventually(func(g Gomega, ctx context.Context) {
				sccRB := &rbacv1.RoleBinding{}
				g.Expect(envTest.Get(ctx, singleNodeSCCRoleBindingName(svcName, testNs.Name), sccRB)).To(Succeed())
				g.Expect(sccRB.Subjects).To(Equal([]rbacv1.Subject{
					{Kind: rbacv1.ServiceAccountKind, Name: mainServiceAccount, Namespace: testNs.Name},
				}))
			}).WithContext(ctx).Should(Succeed())
			expectSCCAccess(ctx, testNs.Name, "default", false)

			// when - decode stops adding capabilities
			updateLLMInferenceService(ctx, llmSvc, func(svc *v1alpha2.LLMInferenceService) {
				svc.Spec.Template = SimpleWorkerPodSpec()
			})

			// then
			Eventually(singleNodeSCCRoleBindingGone(svcName, testNs.Name)).WithContext(ctx).Should(BeTrue())
			expectSCCAccess(ctx, testNs.Name, mainServiceAccount, false)
		})

		It("should delete the single-node SCC RoleBinding when stop annotation is set", func(ctx SpecContext) {
			// given
			svcName := "test-scc-sn-stop"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName))

			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(podAddingCapabilities("IPC_LOCK")),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()
			singleNodeSCCRoleBinding(ctx, svcName, testNs.Name)

			// when
			updateLLMInferenceService(ctx, llmSvc, func(svc *v1alpha2.LLMInferenceService) {
				if svc.Annotations == nil {
					svc.Annotations = make(map[string]string)
				}
				svc.Annotations[constants.StopAnnotationKey] = "true"
			})

			// then
			Eventually(singleNodeSCCRoleBindingGone(svcName, testNs.Name)).WithContext(ctx).Should(BeTrue())
		})

		It("should grant the SCC to the ServiceAccounts of single-node DisaggregatedSet roles", func(ctx SpecContext) {
			// given
			PatchLLMISVCFeatureGates(ctx, envTest.Client, map[string]bool{"disaggregatedSet": true})
			DeferCleanup(func(ctx context.Context) {
				PatchLLMISVCFeatureGates(ctx, envTest.Client, nil)
			})
			svcName := "test-scc-sn-ds"
			testNs := NewTestNamespace(ctx, envTest, WithIstioShadowService(svcName),
				WithServiceAccount("decode-engine"), WithServiceAccount("prefill-engine"))

			decode := podAddingCapabilities("IPC_LOCK")
			decode.ServiceAccountName = "decode-engine"
			prefill := podAddingCapabilities("IPC_LOCK")
			prefill.ServiceAccountName = "prefill-engine"
			llmSvc := LLMInferenceService(svcName,
				InNamespace[*v1alpha2.LLMInferenceService](testNs.Name),
				WithModelURI("hf://facebook/opt-125m"),
				WithTemplate(decode),
				WithPrefill(prefill),
				WithSpecAnnotations(map[string]string{constants.LLMDisaggregatedSetAnnotationKey: "true"}),
				WithManagedRoute(),
				WithManagedGateway(),
			)

			// when
			Expect(envTest.Create(ctx, llmSvc)).To(Succeed())
			defer func() {
				testNs.DeleteAndWait(ctx, llmSvc)
			}()

			// then
			ds := getDisaggregatedSet(ctx, llmSvc)
			Expect(ds.Spec.Roles).To(HaveLen(2))
			Expect(ds.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.ServiceAccountName).To(Equal("decode-engine"))
			Expect(ds.Spec.Roles[1].Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.ServiceAccountName).To(Equal("prefill-engine"))
			sccRB := singleNodeSCCRoleBinding(ctx, svcName, testNs.Name)
			Expect(sccRB.Subjects).To(Equal([]rbacv1.Subject{
				{Kind: rbacv1.ServiceAccountKind, Name: "decode-engine", Namespace: testNs.Name},
				{Kind: rbacv1.ServiceAccountKind, Name: "prefill-engine", Namespace: testNs.Name},
			}))
		})
	})
})

func podAddingCapabilities(capabilities ...corev1.Capability) *corev1.PodSpec {
	pod := SimpleWorkerPodSpec()
	pod.Containers[0].SecurityContext = &corev1.SecurityContext{
		Capabilities: &corev1.Capabilities{Add: capabilities},
	}
	return pod
}

func singleNodeSCCRoleBindingName(svcName, namespace string) types.NamespacedName {
	return types.NamespacedName{Name: kmeta.ChildName(svcName, "-kserve-scc"), Namespace: namespace}
}

func singleNodeSCCRoleBindingGone(svcName, namespace string) func(context.Context) bool {
	return func(ctx context.Context) bool {
		return apierrors.IsNotFound(envTest.Get(ctx, singleNodeSCCRoleBindingName(svcName, namespace), &rbacv1.RoleBinding{}))
	}
}

func singleNodeSCCRoleBinding(ctx context.Context, svcName, namespace string) *rbacv1.RoleBinding {
	GinkgoHelper()
	sccRB := &rbacv1.RoleBinding{}
	Eventually(func(g Gomega, ctx context.Context) error {
		return envTest.Get(ctx, singleNodeSCCRoleBindingName(svcName, namespace), sccRB)
	}).WithContext(ctx).Should(Succeed())
	return sccRB
}

func deploymentServiceAccountName(ctx context.Context, name, namespace string) string {
	GinkgoHelper()
	d := &appsv1.Deployment{}
	Eventually(func(g Gomega, ctx context.Context) error {
		return envTest.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, d)
	}).WithContext(ctx).Should(Succeed())
	return d.Spec.Template.Spec.ServiceAccountName
}

func updateLLMInferenceService(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, mutate func(*v1alpha2.LLMInferenceService)) {
	GinkgoHelper()
	Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &v1alpha2.LLMInferenceService{}
		if err := envTest.Get(ctx, client.ObjectKeyFromObject(llmSvc), current); err != nil {
			return err
		}
		mutate(current)
		return envTest.Update(ctx, current)
	})).To(Succeed())
}

func shippedSCCClusterRole() *rbacv1.ClusterRole {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join(ProjectRoot(), "config/overlays/odh/rbac/llmisvc/llm-svc-scc.yaml"))
	Expect(err).NotTo(HaveOccurred())
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		role := &rbacv1.ClusterRole{}
		err := decoder.Decode(role)
		if errors.Is(err, io.EOF) {
			Fail("llm-svc-scc.yaml has no ClusterRole")
		}
		Expect(err).NotTo(HaveOccurred())
		if role.Kind == "ClusterRole" {
			return role
		}
	}
}

func expectSCCAccess(ctx context.Context, namespace, serviceAccount string, allowed bool) {
	GinkgoHelper()
	clientset, err := kubernetes.NewForConfig(envTest.Config)
	Expect(err).NotTo(HaveOccurred())
	Eventually(func(g Gomega, ctx context.Context) bool {
		review, err := clientset.AuthorizationV1().SubjectAccessReviews().Create(ctx, sccAccessReview(namespace, serviceAccount), metav1.CreateOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		return review.Status.Allowed
	}).WithContext(ctx).Should(Equal(allowed), "%s/%s use of the SCC", namespace, serviceAccount)
}

func sccAccessReview(namespace, serviceAccount string) *authorizationv1.SubjectAccessReview {
	return &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   "system:serviceaccount:" + namespace + ":" + serviceAccount,
			Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"},
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      "use",
				Group:     "security.openshift.io",
				Resource:  "securitycontextconstraints",
				Name:      "openshift-ai-llminferenceservice-scc",
			},
		},
	}
}
