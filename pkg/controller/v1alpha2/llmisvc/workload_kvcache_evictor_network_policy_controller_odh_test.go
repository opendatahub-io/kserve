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

package llmisvc_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
	. "github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
)

var _ = Describe("Standalone PVC evictor NetworkPolicies", func() {
	newService := func(name, namespace string) *v1alpha2.LLMInferenceService {
		svc := LLMInferenceService(name, InNamespace[*v1alpha2.LLMInferenceService](namespace), WithModelURI("hf://facebook/opt-125m"))
		svc.Spec.Template = &corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}, {Name: "pvc-evictor", Image: "example.com/evictor:test"}}}
		svc.Spec.KVCacheOffloading = &v1alpha2.KVCacheOffloadingSpec{
			CPU: resource.MustParse("10Gi"),
			Secondary: []v1alpha2.SecondaryTierSpec{{FileSystem: &v1alpha2.FileSystemTierSpec{PVC: &v1alpha2.PVCTierSpec{
				Ref: &v1alpha2.PVCRefTierSpec{Name: "shared-cache", Path: "cache"},
			}}}},
		}
		return svc
	}
	createPVC := func(ctx context.Context, namespace string) {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "shared-cache", Namespace: namespace},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
			},
		}
		Expect(envTest.Create(ctx, pvc)).To(Succeed())
	}
	waitForEvictor := func(ctx context.Context, namespace string) *appsv1.Deployment {
		list := &appsv1.DeploymentList{}
		Eventually(func(g Gomega) {
			g.Expect(envTest.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{"serving.kserve.io/pvc-evictor": "true"})).To(Succeed())
			g.Expect(list.Items).To(HaveLen(1))
		}).WithContext(ctx).Should(Succeed())
		return list.Items[0].DeepCopy()
	}
	assertDeny := func(policy *netv1.NetworkPolicy, dep *appsv1.Deployment) {
		Expect(policy.Spec.PodSelector).To(Equal(*dep.Spec.Selector))
		Expect(policy.Spec.PolicyTypes).To(ConsistOf(netv1.PolicyTypeIngress, netv1.PolicyTypeEgress))
		Expect(policy.Spec.Ingress).To(BeEmpty())
		Expect(policy.Spec.Egress).To(BeEmpty())
		Expect(metav1.IsControlledBy(policy, dep)).To(BeTrue())
		Expect(policy.OwnerReferences[0].Kind).To(Equal("Deployment"))
	}

	DescribeTable("creates and repairs the Deployment-owned deny policy without rolling the evictor", func(ctx SpecContext, strict bool) {
		PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) { cfg.FeatureGates.StrictNetworkPolicy = strict })
		DeferCleanup(func(ctx SpecContext) {
			PatchLLMISVCConfig(ctx, envTest.Client, func(cfg *llmisvc.LLMISVCConfig) { cfg.FeatureGates.StrictNetworkPolicy = false })
		})
		ns := NewTestNamespace(ctx, envTest)
		createPVC(ctx, ns.Name)
		svc := newService("evictor-policy", ns.Name)
		Expect(envTest.Create(ctx, svc)).To(Succeed())
		defer ns.DeleteAndWait(ctx, svc)
		dep := waitForEvictor(ctx, ns.Name)
		name := kmeta.ChildName(dep.Name, "-networkpolicy-deny-all")
		policy := waitForNetworkPolicy(ctx, ns.Name, name)
		assertDeny(policy, dep)

		// Exercise the policy watch without changing the service or Deployment.
		key := types.NamespacedName{Namespace: ns.Name, Name: name}
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			current := &netv1.NetworkPolicy{}
			if err := envTest.Get(ctx, key, current); err != nil {
				return err
			}
			current.Spec.Egress = []netv1.NetworkPolicyEgressRule{{}}
			current.Labels = nil
			return envTest.Update(ctx, current)
		})).To(Succeed())
		Eventually(func(g Gomega) {
			current := &netv1.NetworkPolicy{}
			g.Expect(envTest.Get(ctx, key, current)).To(Succeed())
			g.Expect(current.Spec.Egress).To(BeEmpty())
			g.Expect(current.Labels).To(HaveKeyWithValue("serving.kserve.io/pvc-evictor", "true"))
		}).WithContext(ctx).Should(Succeed())
		Expect(envTest.Delete(ctx, policy)).To(Succeed())
		policy = waitForNetworkPolicy(ctx, ns.Name, name)
		assertDeny(policy, dep)
		Consistently(func(g Gomega) {
			current := &appsv1.Deployment{}
			g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(dep), current)).To(Succeed())
			g.Expect(current.UID).To(Equal(dep.UID))
			g.Expect(current.Generation).To(Equal(dep.Generation))
			g.Expect(current.Spec.Selector).To(Equal(dep.Spec.Selector))
			g.Expect(current.Spec.Template).To(Equal(dep.Spec.Template))
		}).WithContext(ctx).Should(Succeed())
	}, Entry("in default mode", false), Entry("in strict mode", true))

	It("retains shared coverage until the last active consumer releases the Deployment", func(ctx SpecContext) {
		ns := NewTestNamespace(ctx, envTest)
		createPVC(ctx, ns.Name)
		first, second := newService("first", ns.Name), newService("second", ns.Name)
		Expect(envTest.Create(ctx, first)).To(Succeed())
		Expect(envTest.Create(ctx, second)).To(Succeed())
		defer ns.DeleteAndWait(ctx, first)
		defer ns.DeleteAndWait(ctx, second)
		dep := waitForEvictor(ctx, ns.Name)
		name := kmeta.ChildName(dep.Name, "-networkpolicy-deny-all")
		policy := waitForNetworkPolicy(ctx, ns.Name, name)
		assertDeny(policy, dep)
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			current := &v1alpha2.LLMInferenceService{}
			if err := envTest.Get(ctx, client.ObjectKeyFromObject(first), current); err != nil {
				return err
			}
			if current.Annotations == nil {
				current.Annotations = map[string]string{}
			}
			current.Annotations[constants.StopAnnotationKey] = "true"
			return envTest.Update(ctx, current)
		})).To(Succeed())
		Consistently(func(g Gomega) {
			current := &appsv1.Deployment{}
			g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(dep), current)).To(Succeed())
			g.Expect(current.DeletionTimestamp.IsZero()).To(BeTrue())
			currentPolicy := &netv1.NetworkPolicy{}
			g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(policy), currentPolicy)).To(Succeed())
			g.Expect(currentPolicy.UID).To(Equal(policy.UID))
			g.Expect(metav1.IsControlledBy(currentPolicy, current)).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
		ns.DeleteAndWait(ctx, second)
		Eventually(func(g Gomega) {
			current := &appsv1.Deployment{}
			g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(dep), current)).To(Succeed())
			g.Expect(current.DeletionTimestamp.IsZero()).To(BeFalse())
		}).WithContext(ctx).Should(Succeed())
		// Envtest has no GC. Verify ownership and explicitly simulate external GC;
		// do not claim the controller itself deletes Deployment-owned policies.
		Expect(envTest.Get(ctx, client.ObjectKeyFromObject(policy), policy)).To(Succeed())
		Expect(metav1.IsControlledBy(policy, dep)).To(BeTrue())
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			current := &appsv1.Deployment{}
			if err := envTest.Get(ctx, client.ObjectKeyFromObject(dep), current); err != nil {
				return err
			}
			controllerutil.RemoveFinalizer(current, metav1.FinalizerDeleteDependents)
			return envTest.Update(ctx, current)
		})).To(Succeed())
		Expect(envTest.Delete(ctx, policy)).To(Succeed())
		Consistently(func(g Gomega) {
			g.Expect(apierrors.IsNotFound(envTest.Get(ctx, client.ObjectKeyFromObject(policy), &netv1.NetworkPolicy{}))).To(BeTrue())
		}).WithContext(ctx).Should(Succeed())
	})
})
