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

package testing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	otelv1beta1 "github.com/open-telemetry/opentelemetry-operator/apis/v1beta1"
	istioclientv1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	netv1 "k8s.io/api/networking/v1"
	knservingv1 "knative.dev/serving/pkg/apis/serving/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"

	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// StartWithControllers starts the test environment with the provided controllers.
// It automatically adds necessary CRDs and schemes.
func StartWithControllers(ctrls ...SetupWithManagerFunc) (*Client, context.CancelFunc) {
	// The context passed to Process 1, which is invoked before all parallel nodes are started by Ginkgo,
	// is terminated when this function exits. As a result, this context is unsuitable for use with
	// manager/controllers that need to be available for the entire duration of the test suite.
	// To address this, a new cancellable context must be created to ensure it remains active
	// throughout the whole test suite lifecycle.
	ctx, cancel := context.WithCancel(context.Background())

	return Configure(
		WithCRDs(
			filepath.Join(ProjectRoot(), "test", "crds"),
		),
		WithScheme(
			// KServe Schemes
			v1alpha1.AddToScheme,
			v1beta1.AddToScheme,
			// Kubernetes Schemes
			corev1.AddToScheme,
			appsv1.AddToScheme,
			apiextv1.AddToScheme,
			netv1.AddToScheme,
			gatewayapiv1.Install,
			// Other Schemes
			knservingv1.AddToScheme,
			istioclientv1beta1.AddToScheme,
			kedav1alpha1.AddToScheme,
			otelv1beta1.AddToScheme,
		),
	).WithControllers(ctrls...).
		Start(ctx), cancel
}

// ProjectRoot returns the root directory of the project by searching for the go.mod file up from where it is called.
func ProjectRoot() string {
	rootDir := ""

	currentDir, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("failed to get current working directory: %v", err))
	}

	for {
		if _, err := os.Stat(filepath.Join(currentDir, "go.mod")); err == nil {
			rootDir = filepath.FromSlash(currentDir)

			break
		}

		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			break
		}

		currentDir = parentDir
	}

	if rootDir == "" {
		panic(fmt.Sprintf("failed to get current working directory: %v", err))
	}

	return rootDir
}
