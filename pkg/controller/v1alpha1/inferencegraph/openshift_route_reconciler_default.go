//go:build !distro

/*
Copyright 2023 The KServe Authors.

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

// Package inferencegraph contains the InferenceGraph controller.
// OpenShiftRouteReconciler is defined in openshift_route_reconciler_ocp.go and is
// only available in distro builds. No stub is needed here because all call sites
// that reference OpenShiftRouteReconciler are also in *_ocp.go files.
package inferencegraph
