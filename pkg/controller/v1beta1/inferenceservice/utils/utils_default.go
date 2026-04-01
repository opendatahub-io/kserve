//go:build !distro

/*
Copyright 2021 The KServe Authors.

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

package utils

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetRouteURLIfExists is a no-op stub for non-OpenShift builds.
// OpenShift Route support is only available in the distro build.
func GetRouteURLIfExists(_ context.Context, _ client.Client, _ metav1.ObjectMeta, _ string) (*apis.URL, error) {
	return nil, fmt.Errorf("OpenShift Route support is not available in this build")
}
