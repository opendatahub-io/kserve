//go:build !distro

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

package reconcilers

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

// customizeSharedPVCImportJob is the upstream no-op twin of the ODH shared-PVC import Job
// customization (see sharedpvc_platform_odh.go): upstream builds the import Job without
// platform-specific pod-template labels.
func customizeSharedPVCImportJob(_ *batchv1.Job, _ *v1alpha1.LocalModelNamespaceCache) {}

// extendNamespaceCacheControllerSetup is the upstream no-op twin of the ODH controller
// setup extension (see sharedpvc_platform_odh.go): upstream registers no extra
// owned-resource watches for the namespace cache controller.
func (c *LocalModelNamespaceCacheReconciler) extendNamespaceCacheControllerSetup(_ *builder.Builder) {
}

// reconcileSharedPVCPlatformResources is the upstream no-op twin of the ODH shared-PVC
// platform resource reconciliation (see sharedpvc_platform_odh.go): upstream creates no
// cache-owned NetworkPolicy for import pods.
func (c *LocalModelNamespaceCacheReconciler) reconcileSharedPVCPlatformResources(_ context.Context, _ *v1alpha1.LocalModelNamespaceCache) error {
	return nil
}
