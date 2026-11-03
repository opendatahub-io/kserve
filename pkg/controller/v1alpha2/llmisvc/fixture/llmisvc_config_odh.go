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

package fixture

import (
	"context"
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
)

// PatchLLMISVCConfig merges updates into the live inferenceservice-config llmisvc key.
func PatchLLMISVCConfig(ctx context.Context, c client.Client, mutate func(*llmisvc.LLMISVCConfig)) {
	ginkgo.GinkgoHelper()
	cm := &corev1.ConfigMap{}
	gomega.Expect(c.Get(ctx, types.NamespacedName{
		Name:      constants.InferenceServiceConfigMapName,
		Namespace: constants.KServeNamespace,
	}, cm)).To(gomega.Succeed())
	patch := client.MergeFrom(cm.DeepCopy())

	cfg, err := llmisvc.NewLLMISVCConfig(cm)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	mutate(cfg)

	raw, err := json.Marshal(cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cm.Data["llmisvc"] = string(raw)
	gomega.Expect(c.Patch(ctx, cm, patch)).To(gomega.Succeed())
}

// EnableMonitoringIngressNetworkPolicy turns on the cluster-wide monitoring ingress policy gate.
func EnableMonitoringIngressNetworkPolicy(ctx context.Context, c client.Client) {
	PatchLLMISVCConfig(ctx, c, func(cfg *llmisvc.LLMISVCConfig) {
		cfg.FeatureGates.MonitoringIngressNetworkPolicy = true
	})
}
