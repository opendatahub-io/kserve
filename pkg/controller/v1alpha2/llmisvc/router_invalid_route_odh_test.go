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

package llmisvc

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kserve/kserve/pkg/utils"
)

// The ODH ensureGatewayPreconditions hook checks for the AuthPolicy CRD before
// reconcileRouter gets to the route. Unit tests build the reconciler without a
// rest.Config, so mark the CRD as installed up front instead of letting discovery
// dereference nil.
func init() {
	utils.SetAvailableResourcesForApi(authPolicyGVK.GroupVersion().String(), &metav1.APIResourceList{
		GroupVersion: authPolicyGVK.GroupVersion().String(),
		APIResources: []metav1.APIResource{{Kind: authPolicyGVK.Kind}},
	})
}
