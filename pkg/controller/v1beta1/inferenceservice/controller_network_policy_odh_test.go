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
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
)

func TestControllerNetworkPolicyHostNetworkPeer(t *testing.T) {
	data, err := os.ReadFile("../../../../config/overlays/odh/network-policies.yaml")
	require.NoError(t, err)
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var names []string
	for {
		var policy networkingv1.NetworkPolicy
		err := decoder.Decode(&policy)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		names = append(names, policy.Name)
		t.Run(policy.Name, func(t *testing.T) {
			require.Equal(t, map[string]string{"control-plane": policy.Name}, policy.Spec.PodSelector.MatchLabels)
			require.Len(t, policy.Spec.Ingress, 2)
			hostNetwork := policy.Spec.Ingress[0]
			require.Equal(t, []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
					"policy-group.network.openshift.io/host-network": "",
				}},
			}}, hostNetwork.From, "host-network peers must omit podSelector so OVN uses the special namespace address set")
			require.Equal(t, []networkingv1.NetworkPolicyPort{
				{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt(9443))},
				{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt(8081))},
			}, hostNetwork.Ports)
		})
	}
	require.ElementsMatch(t, []string{"kserve-controller-manager", "llmisvc-controller-manager"}, names)
}
