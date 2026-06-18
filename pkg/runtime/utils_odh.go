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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	goerrors "github.com/pkg/errors"

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

	// Get runtime name from status
	runtimeName := ""
	if isvc.Status.ClusterServingRuntimeName != "" {
		runtimeName = isvc.Status.ClusterServingRuntimeName
	} else if isvc.Status.ServingRuntimeName != "" {
		runtimeName = isvc.Status.ServingRuntimeName
	}

	if runtimeName == "" {
		return "", nil
	}

	// Fetch the runtime and return its server-type annotation
	_, annotations, err, _ := getServingRuntimeFromIsvc(ctx, cl, runtimeName, isvc.Namespace)
	if err != nil {
		return "", err
	}

	return annotations[constants.ServerTypeAnnotationKey], nil
}

// getServingRuntimeFromIsvc Get a ServingRuntime by name. First, ServingRuntimes in the given namespace will be checked.
// If a resource of the specified name is not found, then ClusterServingRuntimes will be checked.
// getServingRuntimeFromIsvc returns the ServingRuntimeSpec, annotations, error, and whether it's a ClusterServingRuntime
// Second value will be the runtime's metadata.annotations
// Fourth value will be true if the ServingRuntime is a ClusterServingRuntime
func getServingRuntimeFromIsvc(ctx context.Context, cl client.Client, name string, namespace string) (*v1alpha1.ServingRuntimeSpec, map[string]string, error, bool) {
	runtime := &v1alpha1.ServingRuntime{}
	err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, runtime)
	if err == nil {
		return &runtime.Spec, runtime.Annotations, nil, false
	} else if !apierrors.IsNotFound(err) {
		return nil, nil, err, false
	}

	clusterRuntime := &v1alpha1.ClusterServingRuntime{}
	err = cl.Get(ctx, client.ObjectKey{Name: name}, clusterRuntime)
	if err == nil {
		return &clusterRuntime.Spec, clusterRuntime.Annotations, nil, true
	} else if !apierrors.IsNotFound(err) && !apimeta.IsNoMatchError(err) {
		return nil, nil, err, false
	}
	return nil, nil, goerrors.New("No ServingRuntimes or ClusterServingRuntimes with the name: " + name), false
}
