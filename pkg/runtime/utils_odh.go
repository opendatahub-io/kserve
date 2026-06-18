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

package runtime

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

// GetServerTypeFromPod reads the server type from the pod's opendatahub.io/kserve-runtime annotation.
// This annotation is propagated from the ServingRuntime during component reconciliation.
// Returns empty string if the annotation is not present.
// This method is intended for use in the pod webhook where the pod is already available.
func GetServerTypeFromPod(pod *corev1.Pod) string {
	if pod == nil || pod.Annotations == nil {
		return ""
	}
	return pod.Annotations[constants.ODHKserveRuntimeAnnotation]
}

// GetServerTypeFromIsvc fetches the runtime for the InferenceService and returns its server-type annotation.
// Returns empty string if:
//   - InferenceService is nil
//   - Runtime name is not populated in status
//   - Runtime doesn't have the serving.kserve.io/server-type annotation
//
// Returns error if runtime fetch fails.
func GetServerTypeFromIsvc(ctx context.Context, cl client.Client, isvc *v1beta1.InferenceService) (string, error) {
	if isvc == nil {
		return "", nil
	}

	// Get runtime name and scope from status
	var runtimeName string
	var isCluster bool
	if isvc.Status.ClusterServingRuntimeName != "" {
		runtimeName = isvc.Status.ClusterServingRuntimeName
		isCluster = true
	} else if isvc.Status.ServingRuntimeName != "" {
		runtimeName = isvc.Status.ServingRuntimeName
		isCluster = false
	}

	if runtimeName == "" {
		return "", nil
	}

	// Fetch the runtime respecting the scope selected in status
	_, annotations, err, _ := getServingRuntime(ctx, cl, runtimeName, isvc.Namespace, isCluster)
	if err != nil {
		return "", err
	}

	return annotations[constants.ServerTypeAnnotationKey], nil
}

// getServingRuntime fetches a ServingRuntime by name, respecting the scope (cluster vs namespaced).
// If isCluster is true, only ClusterServingRuntime is fetched.
// If isCluster is false, only namespaced ServingRuntime is fetched.
// Returns the ServingRuntimeSpec, annotations, error, and whether it's a ClusterServingRuntime.
func getServingRuntime(ctx context.Context, cl client.Client, name string, namespace string, isCluster bool) (*v1alpha1.ServingRuntimeSpec, map[string]string, error, bool) {
	if isCluster {
		// Fetch ClusterServingRuntime
		clusterRuntime := &v1alpha1.ClusterServingRuntime{}
		err := cl.Get(ctx, client.ObjectKey{Name: name}, clusterRuntime)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get ClusterServingRuntime %s: %w", name, err), false
		}
		return &clusterRuntime.Spec, clusterRuntime.Annotations, nil, true
	}

	// Fetch namespaced ServingRuntime
	runtime := &v1alpha1.ServingRuntime{}
	err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, runtime)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get ServingRuntime %s in namespace %s: %w", name, namespace, err), false
	}
	return &runtime.Spec, runtime.Annotations, nil, false
}
