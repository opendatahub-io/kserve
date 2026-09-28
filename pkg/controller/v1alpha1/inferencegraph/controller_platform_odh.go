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

package inferencegraph

import (
	"context"
	"strings"

	osv1 "github.com/openshift/api/route/v1"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

// reconcilePlatformFinalizer manages constants.InferenceGraphFinalizerName. On deletion it
// removes the auth-verifier ServiceAccount from the shared ClusterRoleBinding, which owner
// references cannot garbage-collect, and deletes the ServiceAccount.
func (r *InferenceGraphReconciler) reconcilePlatformFinalizer(ctx context.Context, graph *v1alpha1.InferenceGraph) (bool, error) {
	// examine DeletionTimestamp to determine if object is under deletion
	if graph.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer.
		if !utils.Includes(graph.Finalizers, constants.InferenceGraphFinalizerName) {
			graph.Finalizers = append(graph.Finalizers, constants.InferenceGraphFinalizerName)
			patchYaml := "metadata:\n  finalizers: [" + strings.Join(graph.Finalizers, ",") + "]"
			patchJson, _ := yaml.YAMLToJSON([]byte(patchYaml))
			if err := r.Patch(ctx, graph, client.RawPatch(types.MergePatchType, patchJson)); err != nil {
				return false, err
			}
		}
	} else {
		// The object is being deleted
		if utils.Includes(graph.Finalizers, constants.InferenceGraphFinalizerName) {
			// our finalizer is present, so lets cleanup resources
			if err := r.onDeleteCleanup(ctx, graph); err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried
				return true, err
			}

			// remove our finalizer from the list and update it.
			graph.Finalizers = utils.RemoveString(graph.Finalizers, constants.InferenceGraphFinalizerName)
			patchYaml := "metadata:\n  finalizers: [" + strings.Join(graph.Finalizers, ",") + "]"
			patchJson, _ := yaml.YAMLToJSON([]byte(patchYaml))
			if err := r.Patch(ctx, graph, client.RawPatch(types.MergePatchType, patchJson)); err != nil {
				return true, err
			}
		}

		// Stop reconciliation as the item is being deleted
		return true, nil
	}

	return false, nil
}

func (r *InferenceGraphReconciler) onDeleteCleanup(ctx context.Context, graph *v1alpha1.InferenceGraph) error {
	if err := removeAuthPrivilegesFromGraphServiceAccount(ctx, r.Clientset, graph); err != nil {
		return err
	}
	if err := deleteGraphServiceAccount(ctx, r.Clientset, graph); err != nil {
		return err
	}
	return nil
}

// reconcileRawPlatformPrerequisites reconciles the auth-verifier ServiceAccount and its
// ClusterRoleBinding subject, or removes both when auth is disabled on the graph.
func (r *InferenceGraphReconciler) reconcileRawPlatformPrerequisites(ctx context.Context, graph *v1alpha1.InferenceGraph) error {
	if err := handleInferenceGraphRawAuthResources(ctx, r.Clientset, r.Scheme, graph); err != nil {
		return errors.Wrapf(err, "fails to reconcile resources for auth verification")
	}
	return nil
}

// reconcileRawPlatformNetworking exposes the graph through an OpenShift Route when the Route
// CRD is installed. The returned URL uses https and the admitted Route host, or the
// cluster-local hostname for graphs labelled with cluster-local visibility.
func (r *InferenceGraphReconciler) reconcileRawPlatformNetworking(ctx context.Context, graph *v1alpha1.InferenceGraph, url *apis.URL) (*apis.URL, error) {
	routeAvailable, _ := utils.IsCrdAvailable(r.ClientConfig, osv1.GroupVersion.String(), "Route")
	if routeAvailable {
		routeReconciler := OpenShiftRouteReconciler{
			Scheme: r.Scheme,
			Client: r.Client,
		}
		hostname, err := routeReconciler.Reconcile(ctx, graph)
		url.Host = hostname
		url.Scheme = "https"
		if err != nil {
			return nil, errors.Wrapf(err, "fails to reconcile Route for InferenceGraph")
		}
	}

	return url, nil
}
