/*
Copyright 2025 The KServe Authors.

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
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/utils"
)

var authPolicyGVK = schema.GroupVersionKind{
	Group:   "kuadrant.io",
	Version: "v1",
	Kind:    "AuthPolicy",
}

// validateGatewayOCP checks if Gateway on OCP can be configured correctly.
func (r *LLMInferenceServiceReconciler) validateGatewayOCP(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx).WithName("validateGatewayOCP")

	// If RHCL is not installed, we could end up exposing an LLMInferenceService without authentication, which is a
	// security issue, if that ever happens we need to delete the HTTPRoute immediately to make the service inaccessible.
	//
	// if it gets installed after Kserve startup, the controller needs to be restarted.
	if ok, _ := utils.IsCrdAvailable(r.Config, authPolicyGVK.GroupVersion().String(), authPolicyGVK.Kind); !ok && llmSvc.IsAuthEnabled() {
		route := r.expectedHTTPRoute(ctx, llmSvc)
		if err := Delete(ctx, r, llmSvc, route); err != nil {
			return fmt.Errorf("AuthPolicy CRD is not available, please install Red Hat Connectivity Link: %w", err)
		}
		return errors.New("AuthPolicy CRD is not available, please install Red Hat Connectivity Link")
	}

	logger.Info("Connectivity Link is installed or Auth is disabled", "authEnabled", llmSvc.IsAuthEnabled())

	return nil
}
