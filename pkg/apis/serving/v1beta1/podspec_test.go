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

package v1beta1

import (
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestToCorev1PodSpec(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	scenarios := map[string]struct {
		kservePodSpec PodSpec
		validate      func(*corev1.PodSpec) bool
	}{
		"EmptyPodSpec": {
			kservePodSpec: PodSpec{},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					len(ps.Containers) == 0 &&
					len(ps.InitContainers) == 0 &&
					len(ps.Volumes) == 0
			},
		},
		"PodSpecWithContainers": {
			kservePodSpec: PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "test-image:latest",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					len(ps.Containers) == 1 &&
					ps.Containers[0].Name == "test-container" &&
					ps.Containers[0].Image == "test-image:latest" &&
					ps.Containers[0].Resources.Requests.Cpu().String() == "100m" &&
					ps.Containers[0].Resources.Limits.Memory().String() == "256Mi"
			},
		},
		"PodSpecWithServiceAccount": {
			kservePodSpec: PodSpec{
				ServiceAccountName: "test-sa",
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.ServiceAccountName == "test-sa"
			},
		},
		"PodSpecWithNodeSelector": {
			kservePodSpec: PodSpec{
				NodeSelector: map[string]string{
					"disktype": "ssd",
					"region":   "us-west",
				},
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					len(ps.NodeSelector) == 2 &&
					ps.NodeSelector["disktype"] == "ssd" &&
					ps.NodeSelector["region"] == "us-west"
			},
		},
		"PodSpecWithTolerations": {
			kservePodSpec: PodSpec{
				Tolerations: []corev1.Toleration{
					{
						Key:      "key1",
						Operator: corev1.TolerationOpEqual,
						Value:    "value1",
						Effect:   corev1.TaintEffectNoSchedule,
					},
				},
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					len(ps.Tolerations) == 1 &&
					ps.Tolerations[0].Key == "key1" &&
					ps.Tolerations[0].Value == "value1"
			},
		},
		"PodSpecWithAffinity": {
			kservePodSpec: PodSpec{
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "kubernetes.io/e2e-az-name",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"e2e-az1", "e2e-az2"},
										},
									},
								},
							},
						},
					},
				},
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.Affinity != nil &&
					ps.Affinity.NodeAffinity != nil &&
					ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil &&
					len(ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 1
			},
		},
		"PodSpecWithVolumes": {
			kservePodSpec: PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "test-volume",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				},
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					len(ps.Volumes) == 1 &&
					ps.Volumes[0].Name == "test-volume" &&
					ps.Volumes[0].EmptyDir != nil
			},
		},
		"PodSpecWithImagePullSecrets": {
			kservePodSpec: PodSpec{
				ImagePullSecrets: []corev1.LocalObjectReference{
					{Name: "my-registry-secret"},
				},
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					len(ps.ImagePullSecrets) == 1 &&
					ps.ImagePullSecrets[0].Name == "my-registry-secret"
			},
		},
		"PodSpecWithSecurityContext": {
			kservePodSpec: PodSpec{
				SecurityContext: &corev1.PodSecurityContext{
					RunAsUser:  ptr.To(int64(1000)),
					RunAsGroup: ptr.To(int64(3000)),
					FSGroup:    ptr.To(int64(2000)),
				},
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.SecurityContext != nil &&
					*ps.SecurityContext.RunAsUser == 1000 &&
					*ps.SecurityContext.FSGroup == 2000
			},
		},
		"PodSpecWithInitContainers": {
			kservePodSpec: PodSpec{
				InitContainers: []corev1.Container{
					{
						Name:  "init-container",
						Image: "busybox:latest",
					},
				},
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					len(ps.InitContainers) == 1 &&
					ps.InitContainers[0].Name == "init-container" &&
					ps.InitContainers[0].Image == "busybox:latest"
			},
		},
		"PodSpecWithRestartPolicy": {
			kservePodSpec: PodSpec{
				RestartPolicy: corev1.RestartPolicyAlways,
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.RestartPolicy == corev1.RestartPolicyAlways
			},
		},
		"PodSpecWithTerminationGracePeriod": {
			kservePodSpec: PodSpec{
				TerminationGracePeriodSeconds: ptr.To(int64(30)),
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.TerminationGracePeriodSeconds != nil &&
					*ps.TerminationGracePeriodSeconds == 30
			},
		},
		"PodSpecWithDNSPolicy": {
			kservePodSpec: PodSpec{
				DNSPolicy: corev1.DNSClusterFirst,
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.DNSPolicy == corev1.DNSClusterFirst
			},
		},
		"PodSpecWithPriorityClassName": {
			kservePodSpec: PodSpec{
				PriorityClassName: "high-priority",
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.PriorityClassName == "high-priority"
			},
		},
		"PodSpecWithSchedulerName": {
			kservePodSpec: PodSpec{
				SchedulerName: "custom-scheduler",
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.SchedulerName == "custom-scheduler"
			},
		},
		"PodSpecWithRuntimeClassName": {
			kservePodSpec: PodSpec{
				RuntimeClassName: ptr.To("nvidia"),
				Containers: []corev1.Container{
					{Name: "test"},
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.RuntimeClassName != nil &&
					*ps.RuntimeClassName == "nvidia"
			},
		},
		"ComplexPodSpecWithMultipleFields": {
			kservePodSpec: PodSpec{
				ServiceAccountName: "complex-sa",
				NodeSelector: map[string]string{
					"env": "prod",
				},
				Affinity: &corev1.Affinity{
					PodAntiAffinity: &corev1.PodAntiAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
							{
								LabelSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"app": "myapp",
									},
								},
								TopologyKey: "kubernetes.io/hostname",
							},
						},
					},
				},
				InitContainers: []corev1.Container{
					{Name: "init"},
				},
				Containers: []corev1.Container{
					{
						Name:  "main",
						Image: "app:v1.0",
						Ports: []corev1.ContainerPort{
							{ContainerPort: 8080},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "data-pvc",
							},
						},
					},
				},
				RestartPolicy:                 corev1.RestartPolicyAlways,
				TerminationGracePeriodSeconds: ptr.To(int64(60)),
				DNSPolicy:                     corev1.DNSClusterFirst,
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot: ptr.To(true),
				},
			},
			validate: func(ps *corev1.PodSpec) bool {
				return ps != nil &&
					ps.ServiceAccountName == "complex-sa" &&
					ps.NodeSelector["env"] == "prod" &&
					ps.Affinity != nil &&
					ps.Affinity.PodAntiAffinity != nil &&
					len(ps.InitContainers) == 1 &&
					len(ps.Containers) == 1 &&
					ps.Containers[0].Name == "main" &&
					len(ps.Volumes) == 1 &&
					ps.RestartPolicy == corev1.RestartPolicyAlways &&
					*ps.TerminationGracePeriodSeconds == 60 &&
					ps.SecurityContext != nil &&
					*ps.SecurityContext.RunAsNonRoot == true
			},
		},
	}

	for name, scenario := range scenarios {
		t.Run(name, func(t *testing.T) {
			result := scenario.kservePodSpec.ToCorev1PodSpec()
			if !g.Expect(scenario.validate(&result)).To(gomega.BeTrue()) {
				t.Errorf("Validation failed for scenario %s", name)
			}
		})
	}
}

// TestToCorev1PodSpecFieldMapping ensures all fields are properly mapped
func TestToCorev1PodSpecFieldMapping(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create a PodSpec with all fields populated
	kservePodSpec := PodSpec{
		Volumes:        []corev1.Volume{{Name: "vol"}},
		InitContainers: []corev1.Container{{Name: "init"}},
		Containers:     []corev1.Container{{Name: "main"}},
		EphemeralContainers: []corev1.EphemeralContainer{
			{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "ephemeral"}},
		},
		RestartPolicy:                 corev1.RestartPolicyOnFailure,
		TerminationGracePeriodSeconds: ptr.To(int64(45)),
		ActiveDeadlineSeconds:         ptr.To(int64(600)),
		DNSPolicy:                     corev1.DNSDefault,
		NodeSelector:                  map[string]string{"key": "value"},
		ServiceAccountName:            "sa-name",
		DeprecatedServiceAccount:      "deprecated-sa",
		AutomountServiceAccountToken:  ptr.To(false),
		NodeName:                      "node1",
		HostNetwork:                   true,
		HostPID:                       true,
		HostIPC:                       true,
		ShareProcessNamespace:         ptr.To(true),
		SecurityContext:               &corev1.PodSecurityContext{RunAsUser: ptr.To(int64(1000))},
		ImagePullSecrets:              []corev1.LocalObjectReference{{Name: "secret"}},
		Hostname:                      "hostname",
		Subdomain:                     "subdomain",
		Affinity:                      &corev1.Affinity{},
		SchedulerName:                 "scheduler",
		Tolerations:                   []corev1.Toleration{{Key: "key"}},
		HostAliases:                   []corev1.HostAlias{{IP: "127.0.0.1"}},
		PriorityClassName:             "priority",
		Priority:                      ptr.To(int32(100)),
		DNSConfig:                     &corev1.PodDNSConfig{},
		ReadinessGates:                []corev1.PodReadinessGate{{ConditionType: "Ready"}},
		RuntimeClassName:              ptr.To("runtime"),
		EnableServiceLinks:            ptr.To(false),
		PreemptionPolicy:              (*corev1.PreemptionPolicy)(ptr.To("PreemptLowerPriority")),
		Overhead:                      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
		TopologySpreadConstraints:     []corev1.TopologySpreadConstraint{{MaxSkew: 1}},
		SetHostnameAsFQDN:             ptr.To(true),
		OS:                            &corev1.PodOS{Name: corev1.Linux},
		HostUsers:                     ptr.To(false),
		SchedulingGates:               []corev1.PodSchedulingGate{{Name: "gate"}},
		ResourceClaims:                []corev1.PodResourceClaim{{Name: "claim"}},
		Resources:                     &corev1.ResourceRequirements{},
	}

	result := kservePodSpec.ToCorev1PodSpec()

	// Verify all fields are mapped correctly
	g.Expect(result.Volumes).To(gomega.HaveLen(1))
	g.Expect(result.InitContainers).To(gomega.HaveLen(1))
	g.Expect(result.Containers).To(gomega.HaveLen(1))
	g.Expect(result.EphemeralContainers).To(gomega.HaveLen(1))
	g.Expect(result.RestartPolicy).To(gomega.Equal(corev1.RestartPolicyOnFailure))
	g.Expect(*result.TerminationGracePeriodSeconds).To(gomega.Equal(int64(45)))
	g.Expect(*result.ActiveDeadlineSeconds).To(gomega.Equal(int64(600)))
	g.Expect(result.DNSPolicy).To(gomega.Equal(corev1.DNSDefault))
	g.Expect(result.NodeSelector).To(gomega.HaveLen(1))
	g.Expect(result.ServiceAccountName).To(gomega.Equal("sa-name"))
	g.Expect(result.DeprecatedServiceAccount).To(gomega.Equal("deprecated-sa"))
	g.Expect(*result.AutomountServiceAccountToken).To(gomega.BeFalse())
	g.Expect(result.NodeName).To(gomega.Equal("node1"))
	g.Expect(result.HostNetwork).To(gomega.BeTrue())
	g.Expect(result.HostPID).To(gomega.BeTrue())
	g.Expect(result.HostIPC).To(gomega.BeTrue())
	g.Expect(*result.ShareProcessNamespace).To(gomega.BeTrue())
	g.Expect(result.SecurityContext).NotTo(gomega.BeNil())
	g.Expect(result.ImagePullSecrets).To(gomega.HaveLen(1))
	g.Expect(result.Hostname).To(gomega.Equal("hostname"))
	g.Expect(result.Subdomain).To(gomega.Equal("subdomain"))
	g.Expect(result.Affinity).NotTo(gomega.BeNil())
	g.Expect(result.SchedulerName).To(gomega.Equal("scheduler"))
	g.Expect(result.Tolerations).To(gomega.HaveLen(1))
	g.Expect(result.HostAliases).To(gomega.HaveLen(1))
	g.Expect(result.PriorityClassName).To(gomega.Equal("priority"))
	g.Expect(*result.Priority).To(gomega.Equal(int32(100)))
	g.Expect(result.DNSConfig).NotTo(gomega.BeNil())
	g.Expect(result.ReadinessGates).To(gomega.HaveLen(1))
	g.Expect(*result.RuntimeClassName).To(gomega.Equal("runtime"))
	g.Expect(*result.EnableServiceLinks).To(gomega.BeFalse())
	g.Expect(result.PreemptionPolicy).NotTo(gomega.BeNil())
	g.Expect(result.Overhead).To(gomega.HaveLen(1))
	g.Expect(result.TopologySpreadConstraints).To(gomega.HaveLen(1))
	g.Expect(*result.SetHostnameAsFQDN).To(gomega.BeTrue())
	g.Expect(result.OS).NotTo(gomega.BeNil())
	g.Expect(*result.HostUsers).To(gomega.BeFalse())
	g.Expect(result.SchedulingGates).To(gomega.HaveLen(1))
	g.Expect(result.ResourceClaims).To(gomega.HaveLen(1))
	g.Expect(result.Resources).NotTo(gomega.BeNil())
}
