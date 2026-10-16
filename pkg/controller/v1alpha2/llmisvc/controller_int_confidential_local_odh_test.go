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
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc/fixture"
	"github.com/kserve/kserve/pkg/utils"
)

var _ = Describe("Confidential local block storage", func() {
	It("resumes deployment when an unlabeled local volume and claim become bound", func(ctx SpecContext) {
		ns := fixture.NewTestNamespace(ctx, envTest)
		bundle := confidentialBundle(GinkgoT())
		Expect(envTest.Create(ctx, bundle)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(envTest.Delete(ctx, bundle)).To(Succeed()) })
		preset := readConfidentialPreset(GinkgoT())
		utils.GetInitContainerWithName(preset.Spec.Template, "storage-initializer").Image = "quay.io/kserve/storage-initializer:test"
		Expect(envTest.Create(ctx, preset)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { ns.DeleteAndWait(ctx, preset) })
		configKey := client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}
		original := &corev1.ConfigMap{}
		Expect(envTest.Get(ctx, configKey, original)).To(Succeed())
		raw, existed := original.Data["confidentialBareMetal"]
		DeferCleanup(func(ctx SpecContext) {
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				current := &corev1.ConfigMap{}
				if err := envTest.Get(ctx, configKey, current); err != nil {
					return err
				}
				if existed {
					current.Data["confidentialBareMetal"] = raw
				} else {
					delete(current.Data, "confidentialBareMetal")
				}
				return envTest.Update(ctx, current)
			})).To(Succeed())
		})
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &corev1.ConfigMap{}
			if err := envTest.Get(ctx, configKey, current); err != nil {
				return err
			}
			settings, err := json.Marshal(map[string]string{"luksResourceId": "kbs:///default/local-disk/key"})
			if err != nil {
				return err
			}
			current.Data["confidentialBareMetal"] = string(settings)
			return envTest.Update(ctx, current)
		})).To(Succeed())
		pv, _ := localBlockFixtures()
		pv.Name, pv.Spec.ClaimRef = ns.Name+"-disk", nil
		pv.Status.Phase = corev1.VolumeAvailable
		Expect(envTest.Create(ctx, pv)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(envTest.Delete(ctx, pv)).To(Succeed()) })
		svc := confidentialService(GinkgoT(), "local-model", ns.Name)
		svc.UID = ""
		svc.Annotations = map[string]string{"serving.kserve.io/confidential-pv": pv.Name}
		Expect(envTest.Create(ctx, svc)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { ns.DeleteAndWait(ctx, svc) })
		claimKey := client.ObjectKey{Namespace: ns.Name, Name: "local-model-confidential-storage"}
		claim := &corev1.PersistentVolumeClaim{}
		Eventually(func(g Gomega) {
			g.Expect(envTest.Get(ctx, claimKey, claim)).To(Succeed())
			g.Expect(claim.Spec.VolumeName).To(Equal(pv.Name))
			g.Expect(*claim.Spec.StorageClassName).To(BeEmpty())
			g.Expect(claim.Spec.AccessModes).To(Equal([]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}))
		}).WithContext(ctx).Should(Succeed())
		deploymentKey := client.ObjectKey{Namespace: ns.Name, Name: "local-model-kserve"}
		Consistently(func(g Gomega) {
			g.Expect(envTest.Get(ctx, deploymentKey, &appsv1.Deployment{})).To(MatchError(ContainSubstring("not found")))
		}).WithContext(ctx).Should(Succeed())
		// Existing administrator storage may have no KServe labels. A binding
		// event must still retry the service; envtest has no volume binder.
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := envTest.Get(ctx, claimKey, claim); err != nil {
				return err
			}
			claim.Labels = nil
			return envTest.Update(ctx, claim)
		})).To(Succeed())
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := envTest.Get(ctx, client.ObjectKeyFromObject(pv), pv); err != nil {
				return err
			}
			pv.Spec.ClaimRef = &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: ns.Name, Name: claim.Name, UID: claim.UID}
			return envTest.Update(ctx, pv)
		})).To(Succeed())
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := envTest.Get(ctx, claimKey, claim); err != nil {
				return err
			}
			claim.Status.Phase = corev1.ClaimBound
			return envTest.Status().Update(ctx, claim)
		})).To(Succeed())
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			g.Expect(envTest.Get(ctx, deploymentKey, deployment)).To(Succeed())
			g.Expect(deployment.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
			g.Expect(envTest.Get(ctx, claimKey, claim)).To(Succeed())
			g.Expect(claim.Annotations["serving.kserve.io/confidential-service-uid"]).To(Equal(string(svc.UID)))
		}).WithContext(ctx).Should(Succeed())
	})
})
