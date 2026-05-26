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

package hwprofile

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/constants"
)

// mockClient wraps client.Client, overriding Get to return a configurable error.
type mockClient struct {
	client.Client
	getErr error
}

func (m *mockClient) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return m.getErr
}

// buildUnstructuredHWP builds a HardwareProfile unstructured object for unit tests.
func buildUnstructuredHWP(name, namespace string, spec map[string]interface{}) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": constants.HardwareProfileGroup + "/" + constants.HardwareProfileVersion,
			"kind":       "HardwareProfile",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
	return obj
}

// ---------- Section 1: Resolve() ----------

func TestResolve_EmptyName(t *testing.T) {
	profile, err := Resolve(context.Background(), nil, "", "default")
	assert.NoError(t, err)
	assert.Nil(t, profile)
}

func TestResolve_NotFound(t *testing.T) {
	c := &mockClient{
		getErr: apierrors.NewNotFound(
			schema.GroupResource{Group: constants.HardwareProfileGroup, Resource: constants.HardwareProfileResource},
			"missing-hwp",
		),
	}
	_, err := Resolve(context.Background(), c, "missing-hwp", "test-ns")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-hwp")
	assert.Contains(t, err.Error(), "test-ns")
}

func TestResolve_FetchError(t *testing.T) {
	c := &mockClient{
		getErr: errors.New("connection timeout"),
	}
	_, err := Resolve(context.Background(), c, "some-hwp", "test-ns")
	require.Error(t, err)
}

// ---------- Section 2: parseProfile() ----------

func TestParseProfile_ResourceIdentifiers(t *testing.T) {
	obj := buildUnstructuredHWP("test", "ns", map[string]interface{}{
		"identifiers": []interface{}{
			map[string]interface{}{"identifier": "cpu", "defaultCount": "4"},
			map[string]interface{}{"identifier": "nvidia.com/gpu", "defaultCount": "2"},
		},
	})

	profile, err := parseProfile(context.Background(), obj)
	require.NoError(t, err)
	require.Len(t, profile.Identifiers, 2)

	cpuIdx := -1
	gpuIdx := -1
	for i, id := range profile.Identifiers {
		if id.ResourceName == corev1.ResourceName("cpu") {
			cpuIdx = i
		}
		if id.ResourceName == corev1.ResourceName("nvidia.com/gpu") {
			gpuIdx = i
		}
	}
	require.GreaterOrEqual(t, cpuIdx, 0, "cpu identifier not found")
	require.GreaterOrEqual(t, gpuIdx, 0, "GPU identifier not found")

	assert.Equal(t, resource.MustParse("4"), profile.Identifiers[cpuIdx].DefaultCount)
	assert.Equal(t, resource.MustParse("2"), profile.Identifiers[gpuIdx].DefaultCount)
}

func TestParseProfile_DefaultCountMissing(t *testing.T) {
	obj := buildUnstructuredHWP("test", "ns", map[string]interface{}{
		"identifiers": []interface{}{
			map[string]interface{}{"identifier": "cpu"},
		},
	})

	profile, err := parseProfile(context.Background(), obj)
	require.NoError(t, err)
	require.Len(t, profile.Identifiers, 1)
	// Use Cmp for semantic equality since the Quantity string cache may differ.
	assert.Equal(t, 0, profile.Identifiers[0].DefaultCount.Cmp(resource.MustParse("1")),
		"expected defaultCount to be 1, got %s", profile.Identifiers[0].DefaultCount.String())
}

func TestParseProfile_InvalidDefaultCount(t *testing.T) {
	obj := buildUnstructuredHWP("test", "ns", map[string]interface{}{
		"identifiers": []interface{}{
			map[string]interface{}{"identifier": "cpu", "defaultCount": "bad!"},
		},
	})

	profile, err := parseProfile(context.Background(), obj)
	require.NoError(t, err)
	assert.Empty(t, profile.Identifiers, "invalid identifier should be skipped")
}

func TestParseProfile_KueueScheduling(t *testing.T) {
	obj := buildUnstructuredHWP("test", "ns", map[string]interface{}{
		"schedulingSpec": map[string]interface{}{
			"type": "Queue",
			"kueue": map[string]interface{}{
				"localQueueName": "my-queue",
			},
		},
	})

	profile, err := parseProfile(context.Background(), obj)
	require.NoError(t, err)
	assert.Equal(t, "my-queue", profile.KueueQueueName)
	assert.Nil(t, profile.NodeSelector)
	assert.Nil(t, profile.Tolerations)
}

func TestParseProfile_NodeScheduling(t *testing.T) {
	obj := buildUnstructuredHWP("test", "ns", map[string]interface{}{
		"schedulingSpec": map[string]interface{}{
			"type": "Node",
			"node": map[string]interface{}{
				"nodeSelector": map[string]interface{}{
					"k": "v",
				},
				"tolerations": []interface{}{
					map[string]interface{}{
						"key":      "t",
						"operator": "Exists",
						"effect":   "NoSchedule",
					},
				},
			},
		},
	})

	profile, err := parseProfile(context.Background(), obj)
	require.NoError(t, err)
	assert.Equal(t, "", profile.KueueQueueName)
	require.Len(t, profile.NodeSelector, 1)
	assert.Equal(t, "v", profile.NodeSelector["k"])
	require.Len(t, profile.Tolerations, 1)
	assert.Equal(t, "t", profile.Tolerations[0].Key)
	assert.Equal(t, corev1.TolerationOperator("Exists"), profile.Tolerations[0].Operator)
	assert.Equal(t, corev1.TaintEffect("NoSchedule"), profile.Tolerations[0].Effect)
}

func TestParseProfile_NodeSchedulingWithTolerationSeconds(t *testing.T) {
	obj := buildUnstructuredHWP("test", "ns", map[string]interface{}{
		"schedulingSpec": map[string]interface{}{
			"type": "Node",
			"node": map[string]interface{}{
				"tolerations": []interface{}{
					map[string]interface{}{
						"key":               "t",
						"operator":          "Exists",
						"effect":            "NoSchedule",
						"tolerationSeconds": int64(300),
					},
				},
			},
		},
	})

	profile, err := parseProfile(context.Background(), obj)
	require.NoError(t, err)
	require.Len(t, profile.Tolerations, 1)
	require.NotNil(t, profile.Tolerations[0].TolerationSeconds)
	assert.Equal(t, int64(300), *profile.Tolerations[0].TolerationSeconds)
}

func TestParseProfile_NoSchedulingSpec(t *testing.T) {
	obj := buildUnstructuredHWP("test", "ns", map[string]interface{}{
		"identifiers": []interface{}{
			map[string]interface{}{"identifier": "cpu", "defaultCount": "4"},
		},
	})

	profile, err := parseProfile(context.Background(), obj)
	require.NoError(t, err)
	assert.Equal(t, "", profile.KueueQueueName)
	assert.Nil(t, profile.NodeSelector)
	assert.Nil(t, profile.Tolerations)
}

func TestParseProfile_EmptySpec(t *testing.T) {
	obj := buildUnstructuredHWP("test", "ns", map[string]interface{}{})

	profile, err := parseProfile(context.Background(), obj)
	require.NoError(t, err)
	assert.Empty(t, profile.Identifiers)
	assert.Equal(t, "", profile.KueueQueueName)
	assert.Nil(t, profile.NodeSelector)
	assert.Nil(t, profile.Tolerations)
}

// ---------- Section 3: ApplyToContainerResources() ----------

func newPodSpecWithContainer(name string, resources corev1.ResourceRequirements) corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: name, Resources: resources},
		},
	}
}

func TestApplyToContainerResources_MatchingContainer(t *testing.T) {
	profile := &ResolvedProfile{
		Identifiers: []ResourceIdentifier{
			{ResourceName: "cpu", DefaultCount: resource.MustParse("4")},
			{ResourceName: "nvidia.com/gpu", DefaultCount: resource.MustParse("2")},
		},
	}
	podSpec := newPodSpecWithContainer(constants.InferenceServiceContainerName, corev1.ResourceRequirements{})

	ApplyToContainerResources(context.Background(), profile, constants.InferenceServiceContainerName, &podSpec)

	c := podSpec.Containers[0]
	assert.Equal(t, resource.MustParse("4"), c.Resources.Requests["cpu"])
	assert.Equal(t, resource.MustParse("4"), c.Resources.Limits["cpu"])
	assert.Equal(t, resource.MustParse("2"), c.Resources.Requests["nvidia.com/gpu"])
	assert.Equal(t, resource.MustParse("2"), c.Resources.Limits["nvidia.com/gpu"])
}

func TestApplyToContainerResources_ResourceAlreadyInRequests(t *testing.T) {
	profile := &ResolvedProfile{
		Identifiers: []ResourceIdentifier{
			{ResourceName: "cpu", DefaultCount: resource.MustParse("4")},
		},
	}
	existing := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{"cpu": resource.MustParse("1")},
	}
	podSpec := newPodSpecWithContainer(constants.InferenceServiceContainerName, existing)

	ApplyToContainerResources(context.Background(), profile, constants.InferenceServiceContainerName, &podSpec)

	// Existing request takes priority; should not be overwritten
	assert.Equal(t, resource.MustParse("1"), podSpec.Containers[0].Resources.Requests["cpu"])
}

func TestApplyToContainerResources_ResourceAlreadyInLimits(t *testing.T) {
	profile := &ResolvedProfile{
		Identifiers: []ResourceIdentifier{
			{ResourceName: "nvidia.com/gpu", DefaultCount: resource.MustParse("2")},
		},
	}
	existing := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
	}
	podSpec := newPodSpecWithContainer(constants.InferenceServiceContainerName, existing)

	ApplyToContainerResources(context.Background(), profile, constants.InferenceServiceContainerName, &podSpec)

	// Existing limit takes priority; should not be overwritten
	assert.Equal(t, resource.MustParse("1"), podSpec.Containers[0].Resources.Limits["nvidia.com/gpu"])
}

func TestApplyToContainerResources_ContainerNotFound(t *testing.T) {
	profile := &ResolvedProfile{
		Identifiers: []ResourceIdentifier{
			{ResourceName: "cpu", DefaultCount: resource.MustParse("4")},
		},
	}
	podSpec := newPodSpecWithContainer("other-container", corev1.ResourceRequirements{})

	// Should not panic or error
	ApplyToContainerResources(context.Background(), profile, constants.InferenceServiceContainerName, &podSpec)

	// other-container is unchanged
	assert.Nil(t, podSpec.Containers[0].Resources.Requests)
}

func TestApplyToContainerResources_NilProfile(t *testing.T) {
	podSpec := newPodSpecWithContainer(constants.InferenceServiceContainerName, corev1.ResourceRequirements{})
	original := podSpec.DeepCopy()

	ApplyToContainerResources(context.Background(), nil, constants.InferenceServiceContainerName, &podSpec)

	assert.Equal(t, *original, podSpec)
}

func TestApplyToContainerResources_EmptyIdentifiers(t *testing.T) {
	profile := &ResolvedProfile{Identifiers: nil}
	podSpec := newPodSpecWithContainer(constants.InferenceServiceContainerName, corev1.ResourceRequirements{})
	original := podSpec.DeepCopy()

	ApplyToContainerResources(context.Background(), profile, constants.InferenceServiceContainerName, &podSpec)

	assert.Equal(t, *original, podSpec)
}

func TestApplyToContainerResources_InitializesNilResourceList(t *testing.T) {
	profile := &ResolvedProfile{
		Identifiers: []ResourceIdentifier{
			{ResourceName: "cpu", DefaultCount: resource.MustParse("4")},
		},
	}
	// Container with nil Requests and Limits
	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: constants.InferenceServiceContainerName},
		},
	}

	ApplyToContainerResources(context.Background(), profile, constants.InferenceServiceContainerName, &podSpec)

	assert.Equal(t, resource.MustParse("4"), podSpec.Containers[0].Resources.Requests["cpu"])
	assert.Equal(t, resource.MustParse("4"), podSpec.Containers[0].Resources.Limits["cpu"])
}

// ---------- Section 4: ApplyNodeScheduling() ----------

func TestApplyNodeScheduling_EmptyPodSpec(t *testing.T) {
	profile := &ResolvedProfile{
		NodeSelector: map[string]string{"zone": "us-east"},
		Tolerations: []corev1.Toleration{
			{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		},
	}
	podSpec := &corev1.PodSpec{}

	ApplyNodeScheduling(profile, podSpec)

	assert.Equal(t, "us-east", podSpec.NodeSelector["zone"])
	require.Len(t, podSpec.Tolerations, 1)
	assert.Equal(t, "nvidia.com/gpu", podSpec.Tolerations[0].Key)
}

func TestApplyNodeScheduling_ExistingKeyPreserved(t *testing.T) {
	profile := &ResolvedProfile{
		NodeSelector: map[string]string{"zone": "eu-west"},
	}
	podSpec := &corev1.PodSpec{
		NodeSelector: map[string]string{"zone": "us-east"},
	}

	ApplyNodeScheduling(profile, podSpec)

	assert.Equal(t, "us-east", podSpec.NodeSelector["zone"], "existing key should not be overwritten")
}

func TestApplyNodeScheduling_HWPOnlyKeyAdded(t *testing.T) {
	profile := &ResolvedProfile{
		NodeSelector: map[string]string{"zone": "eu-west", "tier": "gpu"},
	}
	podSpec := &corev1.PodSpec{
		NodeSelector: map[string]string{"tier": "gpu"},
	}

	ApplyNodeScheduling(profile, podSpec)

	assert.Equal(t, "gpu", podSpec.NodeSelector["tier"])
	assert.Equal(t, "eu-west", podSpec.NodeSelector["zone"], "HWP-only key should be added")
}

func TestApplyNodeScheduling_TolerationDeduplication(t *testing.T) {
	tol := corev1.Toleration{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	profile := &ResolvedProfile{
		Tolerations: []corev1.Toleration{tol},
	}
	podSpec := &corev1.PodSpec{
		Tolerations: []corev1.Toleration{tol},
	}

	ApplyNodeScheduling(profile, podSpec)

	assert.Len(t, podSpec.Tolerations, 1, "duplicate toleration should not be added")
}

func TestApplyNodeScheduling_NewTolerationAdded(t *testing.T) {
	profile := &ResolvedProfile{
		Tolerations: []corev1.Toleration{
			{Key: "B", Operator: corev1.TolerationOpExists},
		},
	}
	podSpec := &corev1.PodSpec{
		Tolerations: []corev1.Toleration{
			{Key: "A", Operator: corev1.TolerationOpExists},
		},
	}

	ApplyNodeScheduling(profile, podSpec)

	assert.Len(t, podSpec.Tolerations, 2)
}

func TestApplyNodeScheduling_NilProfile(t *testing.T) {
	podSpec := &corev1.PodSpec{NodeSelector: map[string]string{"zone": "us-east"}}

	ApplyNodeScheduling(nil, podSpec)

	assert.Equal(t, "us-east", podSpec.NodeSelector["zone"])
}

func TestApplyNodeScheduling_EmptyNodeScheduling(t *testing.T) {
	profile := &ResolvedProfile{NodeSelector: nil, Tolerations: nil}
	podSpec := &corev1.PodSpec{NodeSelector: map[string]string{"zone": "us-east"}}

	ApplyNodeScheduling(profile, podSpec)

	assert.Equal(t, "us-east", podSpec.NodeSelector["zone"])
	assert.Empty(t, podSpec.Tolerations)
}

// ---------- Section 5: ApplyKueueLabel() ----------

func TestApplyKueueLabel_LabelNotSet(t *testing.T) {
	profile := &ResolvedProfile{KueueQueueName: "my-queue"}
	meta := &metav1.ObjectMeta{Labels: map[string]string{"existing": "label"}}

	ApplyKueueLabel(profile, meta)

	assert.Equal(t, "my-queue", meta.Labels[constants.KueueQueueNameLabel])
}

func TestApplyKueueLabel_InitialisesNilLabels(t *testing.T) {
	profile := &ResolvedProfile{KueueQueueName: "my-queue"}
	meta := &metav1.ObjectMeta{}

	ApplyKueueLabel(profile, meta)

	require.NotNil(t, meta.Labels)
	assert.Equal(t, "my-queue", meta.Labels[constants.KueueQueueNameLabel])
}

func TestApplyKueueLabel_ExistingLabelPreserved(t *testing.T) {
	profile := &ResolvedProfile{KueueQueueName: "hwp-queue"}
	meta := &metav1.ObjectMeta{
		Labels: map[string]string{constants.KueueQueueNameLabel: "user-queue"},
	}

	ApplyKueueLabel(profile, meta)

	assert.Equal(t, "user-queue", meta.Labels[constants.KueueQueueNameLabel], "existing label should not be overwritten")
}

func TestApplyKueueLabel_NilProfile(t *testing.T) {
	meta := &metav1.ObjectMeta{Labels: map[string]string{"k": "v"}}

	ApplyKueueLabel(nil, meta)

	assert.NotContains(t, meta.Labels, constants.KueueQueueNameLabel)
}

func TestApplyKueueLabel_EmptyQueueName(t *testing.T) {
	profile := &ResolvedProfile{KueueQueueName: ""}
	meta := &metav1.ObjectMeta{}

	ApplyKueueLabel(profile, meta)

	assert.Nil(t, meta.Labels)
}

// ---------- Section 6: HardwareProfileRef() ----------

func TestHardwareProfileRef_BothAnnotations(t *testing.T) {
	annotations := map[string]string{
		constants.HardwareProfileAnnotationName:      "my-hwp",
		constants.HardwareProfileAnnotationNamespace: "odh",
	}

	name, namespace := HardwareProfileRef(annotations, "default")

	assert.Equal(t, "my-hwp", name)
	assert.Equal(t, "odh", namespace)
}

func TestHardwareProfileRef_NamespaceAnnotationAbsent(t *testing.T) {
	annotations := map[string]string{
		constants.HardwareProfileAnnotationName: "my-hwp",
	}

	name, namespace := HardwareProfileRef(annotations, "default")

	assert.Equal(t, "my-hwp", name)
	assert.Equal(t, "default", namespace)
}

func TestHardwareProfileRef_NameAnnotationAbsent(t *testing.T) {
	annotations := map[string]string{}

	name, namespace := HardwareProfileRef(annotations, "default")

	assert.Equal(t, "", name)
	assert.Equal(t, "default", namespace)
}

func TestHardwareProfileRef_NilAnnotations(t *testing.T) {
	name, namespace := HardwareProfileRef(nil, "default")

	assert.Equal(t, "", name)
	assert.Equal(t, "default", namespace)
}

