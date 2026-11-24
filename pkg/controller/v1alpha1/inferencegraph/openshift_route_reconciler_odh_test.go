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
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

func TestBuildOpenShiftRouteTLS(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	require.NoError(t, routev1.Install(scheme))

	graph := odhTestGraph(nil, "")
	reconciler := &OpenShiftRouteReconciler{Scheme: scheme}

	withTLS, err := reconciler.buildOpenShiftRoute(graph, true)
	require.NoError(t, err)
	require.Equal(t, graph.Name+"-route", withTLS.Name)
	require.NotNil(t, withTLS.Spec.TLS)
	require.Equal(t, routev1.TLSTerminationReencrypt, withTLS.Spec.TLS.Termination)

	plain, err := reconciler.buildOpenShiftRoute(graph, false)
	require.NoError(t, err)
	require.Nil(t, plain.Spec.TLS)
	require.Equal(t, withTLS.Spec.To, plain.Spec.To)
	require.Equal(t, withTLS.Spec.Port, plain.Spec.Port)
}

func TestReconcileUsesTheTLSDecisionItIsGiven(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	require.NoError(t, routev1.Install(scheme))

	graph := odhTestGraph(map[string]string{constants.ODHKserveEnableTLS: "false"}, "")
	cl := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &OpenShiftRouteReconciler{Scheme: scheme, Client: cl}

	_, err := reconciler.Reconcile(context.Background(), graph, false)
	require.NoError(t, err)

	got := &routev1.Route{}
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{
		Name:      graph.Name + "-route",
		Namespace: graph.Namespace,
	}, got))
	require.Nil(t, got.Spec.TLS)
}

func TestReconcileDoesNotExposeAuthenticatedRouterOverHTTP(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	require.NoError(t, routev1.Install(scheme))

	graph := odhTestGraph(map[string]string{
		constants.ODHKserveEnableTLS: "false",
		constants.ODHKserveRawAuth:   "true",
	}, "")
	cl := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &OpenShiftRouteReconciler{Scheme: scheme, Client: cl}

	_, err := reconciler.Reconcile(context.Background(), graph, false)
	require.ErrorContains(t, err, "require TLS")

	err = cl.Get(context.Background(), types.NamespacedName{
		Name:      graph.Name + "-route",
		Namespace: graph.Namespace,
	}, &routev1.Route{})
	require.True(t, apierrors.IsNotFound(err))
}
