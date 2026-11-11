/*
Copyright 2024 The KServe Authors.

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

package distro

// Distro-specific RBAC for the LocalModel controller.
// Processed by a separate controller-gen invocation (see Makefile.overrides.mk)
// to generate a dedicated ClusterRole included only in distro overlays.

//+kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
//+kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// The cache-owned import egress NetworkPolicy (see reconcilers/sharedpvc_platform_odh.go)
// is ensured with get/create/update and watched for delete/drift self-healing. No patch
// or delete: teardown is owner-reference garbage collection with the cache.
//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update
//+kubebuilder:rbac:groups=serving.kserve.io,resources=localmodelcaches/finalizers,verbs=update
//+kubebuilder:rbac:groups=serving.kserve.io,resources=localmodelnamespacecaches/finalizers,verbs=update
