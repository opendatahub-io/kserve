//go:build !distro

package inferencegraph

import (
	"context"

	routev1 "github.com/openshift/api/route/v1"
	"k8s.io/apimachinery/pkg/types"
)

func admitRouteForURLTest(_ context.Context, _ *routev1.Route, _ types.NamespacedName) {}
