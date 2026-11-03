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
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
	"github.com/kserve/kserve/pkg/utils"
)

var _ = Describe("Bare-metal confidential serving", func() {
	It("uses customer claims, repairs shared helpers, and leaves storage intact on deletion", func(ctx SpecContext) {
		ns := fixture.NewTestNamespace(ctx, envTest)
		secondNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-confidential-other-"}}
		Expect(envTest.Create(ctx, secondNS)).To(Succeed())
		Expect(envTest.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: secondNS.Name}})).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { envTest.DeleteAll(ctx, secondNS) })
		bundle := confidentialBundle(GinkgoT())
		Expect(envTest.Create(ctx, bundle)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(envTest.Delete(ctx, bundle)).To(Succeed()) })
		preset := readConfidentialPreset(GinkgoT())
		utils.GetInitContainerWithName(preset.Spec.Template, "storage-initializer").Image = "quay.io/kserve/storage-initializer:test"
		Expect(envTest.Create(ctx, preset)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { ns.DeleteAndWait(ctx, preset) })
		key := client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}
		original := &corev1.ConfigMap{}
		Expect(envTest.Get(ctx, key, original)).To(Succeed())
		raw, existed := original.Data["confidentialBareMetal"]
		DeferCleanup(func(ctx SpecContext) {
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				cm := &corev1.ConfigMap{}
				if err := envTest.Get(ctx, key, cm); err != nil {
					return err
				}
				if existed {
					cm.Data["confidentialBareMetal"] = raw
				} else {
					delete(cm.Data, "confidentialBareMetal")
				}
				return envTest.Update(ctx, cm)
			})).To(Succeed())
		})
		settings, err := json.Marshal(map[string]string{"luksResourceId": "kbs:///default/disk/key"})
		Expect(err).NotTo(HaveOccurred())
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cm := &corev1.ConfigMap{}
			if err := envTest.Get(ctx, key, cm); err != nil {
				return err
			}
			cm.Data["confidentialBareMetal"] = string(settings)
			return envTest.Update(ctx, cm)
		})).To(Succeed())
		services := []*v1alpha2.LLMInferenceService{
			confidentialService(GinkgoT(), "first", ns.Name),
			confidentialService(GinkgoT(), "second", ns.Name),
			confidentialService(GinkgoT(), "third", secondNS.Name),
		}
		customerClaims := map[string]*corev1.PersistentVolumeClaim{}
		for i, svc := range services {
			claim := confidentialClaim(svc)
			if i == 0 {
				// A classless local claim can remain pending while the customer
				// prepares its PV. KServe must not inspect or wait for that PV.
				claim.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
				claim.Spec.StorageClassName = ptr.To("")
				claim.Spec.VolumeName = ns.Name + "-customer-disk"
			} else {
				Expect(envTest.Create(ctx, claim)).To(Succeed())
			}
			customerClaims[svc.Name] = claim
			svc.UID = ""
			Expect(envTest.Create(ctx, svc)).To(Succeed())
			DeferCleanup(func(ctx SpecContext) { ns.DeleteAndWait(ctx, svc) })
		}

		By("waiting for the customer to supply the selected PVC")
		first := services[0]
		Eventually(func(g Gomega) {
			current := &v1alpha2.LLMInferenceService{}
			g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(first), current)).To(Succeed())
			g.Expect(current.Status.Conditions).To(ContainElement(WithTransform(func(c apis.Condition) string { return c.Message }, ContainSubstring("create the storage before deploying"))))
			g.Expect(envTest.Get(ctx, client.ObjectKeyFromObject(customerClaims[first.Name]), &corev1.PersistentVolumeClaim{})).To(MatchError(ContainSubstring("not found")))
			g.Expect(envTest.Get(ctx, client.ObjectKey{Namespace: first.Namespace, Name: first.Name + "-kserve"}, &appsv1.Deployment{})).To(MatchError(ContainSubstring("not found")))
		}).WithContext(ctx).Should(Succeed())
		// This unlabeled, unbound claim must trigger reconciliation without a PV.
		Expect(envTest.Create(ctx, customerClaims[first.Name])).To(Succeed())

		assertDeployment := func(g Gomega, svc *v1alpha2.LLMInferenceService) {
			d := &appsv1.Deployment{}
			g.Expect(envTest.Get(ctx, client.ObjectKey{Namespace: svc.Namespace, Name: svc.Name + "-kserve"}, d)).To(Succeed())
			g.Expect(d.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
			g.Expect(d.Spec.Template.Spec.ServiceAccountName).To(Equal("coco-cc-storage"))
			init := utils.GetInitContainerWithName(&d.Spec.Template.Spec, "storage-initializer")
			g.Expect(init).NotTo(BeNil())
			g.Expect(init.Args).To(Equal([]string{svc.Spec.Model.URI.String(), "/mnt/models"}))
			claim := &corev1.PersistentVolumeClaim{}
			g.Expect(envTest.Get(ctx, client.ObjectKey{Namespace: svc.Namespace, Name: svc.Name + "-model-disk"}, claim)).To(Succeed())
			g.Expect(claim.Spec).To(Equal(customerClaims[svc.Name].Spec))
			g.Expect(claim.OwnerReferences).To(BeEmpty())
			g.Expect(claim.ResourceVersion).To(Equal(customerClaims[svc.Name].ResourceVersion))
			g.Expect(claim.Annotations).To(BeEmpty())
			volumeFound := false
			for _, volume := range d.Spec.Template.Spec.Volumes {
				if volume.Name == "luks-block" {
					g.Expect(volume.PersistentVolumeClaim.ClaimName).To(Equal(claim.Name))
					volumeFound = true
				}
			}
			g.Expect(volumeFound).To(BeTrue())
			binding := &rbacv1.RoleBinding{}
			g.Expect(envTest.Get(ctx, client.ObjectKey{Namespace: svc.Namespace, Name: "coco-cc-storage"}, binding)).To(Succeed())
			g.Expect(binding.Subjects[0].Namespace).To(Equal(svc.Namespace))
			g.Expect(binding.OwnerReferences).To(BeEmpty())
		}
		Eventually(func(g Gomega) {
			for _, svc := range services {
				assertDeployment(g, svc)
			}
		}).WithContext(ctx).Should(Succeed())

		By("repairing a deleted shared helper through a dependency watch")
		helper := &corev1.ConfigMap{}
		helperKey := client.ObjectKey{Namespace: ns.Name, Name: "cc-storage-link"}
		Expect(envTest.Get(ctx, helperKey, helper)).To(Succeed())
		previousUID := helper.UID
		Expect(envTest.Delete(ctx, helper)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(envTest.Get(ctx, helperKey, helper)).To(Succeed())
			g.Expect(helper.UID).NotTo(Equal(previousUID))
		}).WithContext(ctx).Should(Succeed())

		By("applying changed installation defaults through the ConfigMap watch")
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cm := &corev1.ConfigMap{}
			if err := envTest.Get(ctx, key, cm); err != nil {
				return err
			}
			data := map[string]string{}
			if err := json.Unmarshal([]byte(cm.Data["confidentialBareMetal"]), &data); err != nil {
				return err
			}
			data["initData"] = "updated-initdata"
			encoded, err := json.Marshal(data)
			if err != nil {
				return err
			}
			cm.Data["confidentialBareMetal"] = string(encoded)
			return envTest.Update(ctx, cm)
		})).To(Succeed())
		Eventually(func(g Gomega) {
			d := &appsv1.Deployment{}
			g.Expect(envTest.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "second-kserve"}, d)).To(Succeed())
			g.Expect(d.Spec.Template.Annotations["io.katacontainers.config.hypervisor.cc_init_data"]).To(Equal("updated-initdata"))
		}).WithContext(ctx).Should(Succeed())

		By("deleting one service while retaining its claim and the other service's helpers")
		ns.DeleteAndWait(ctx, services[0])
		Eventually(func(g Gomega) {
			assertDeployment(g, services[1])
			claim := &corev1.PersistentVolumeClaim{}
			g.Expect(envTest.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "first-model-disk"}, claim)).To(Succeed())
			g.Expect(envTest.Get(ctx, helperKey, helper)).To(Succeed())
		}).WithContext(ctx).Should(Succeed())
	})
})
