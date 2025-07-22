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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

// RefValidationIssueType represents the type of validation issue
type RefValidationIssueType string

const (
	RefsNotFoundReason      RefValidationIssueType = "RefsNotFound"
	RefsMisconfiguredReason RefValidationIssueType = "RefsMisconfigured"
)

// RefValidationIssue represents a single validation issue
type RefValidationIssue struct {
	Type    RefValidationIssueType
	Message string
}

// RefValidationResult represents the result of validating reference components
type RefValidationResult struct {
	issues []RefValidationIssue
}

// AddNotFoundIssue adds a "resource not found" validation issue
func (v *RefValidationResult) AddNotFoundIssue(message string) {
	if message != "" {
		v.issues = append(v.issues, RefValidationIssue{
			Type:    RefsNotFoundReason,
			Message: message,
		})
	}
}

// AddMisconfiguredIssue adds a "resource misconfigured" validation issue
func (v *RefValidationResult) AddMisconfiguredIssue(message string) {
	if message != "" {
		v.issues = append(v.issues, RefValidationIssue{
			Type:    RefsMisconfiguredReason,
			Message: message,
		})
	}
}

// IsValid returns true if there are no validation issues
func (v *RefValidationResult) IsValid() bool {
	return len(v.issues) == 0
}

// HasNotFoundIssues returns true if there are any "not found" issues
func (v *RefValidationResult) HasNotFoundIssues() bool {
	for _, issue := range v.issues {
		if issue.Type == RefsNotFoundReason {
			return true
		}
	}
	return false
}

// HasMisconfiguredIssues returns true if there are any "misconfigured" issues
func (v *RefValidationResult) HasMisconfiguredIssues() bool {
	for _, issue := range v.issues {
		if issue.Type == RefsMisconfiguredReason {
			return true
		}
	}
	return false
}

// CombinedMessage returns all validation issues as a bulleted list
func (v *RefValidationResult) CombinedMessage() string {
	if len(v.issues) == 0 {
		return ""
	}
	if len(v.issues) == 1 {
		return v.issues[0].Message
	}

	var message strings.Builder
	for _, issue := range v.issues {
		message.WriteString("- ")
		message.WriteString(issue.Message)
		message.WriteString("\n")
	}
	return strings.TrimSuffix(message.String(), "\n")
}

// AllIssues returns all validation issues
func (v *RefValidationResult) AllIssues() []RefValidationIssue {
	return v.issues
}

// validateHTTPRouteRefs validates HTTP route references and returns found routes
func (r *LLMInferenceServiceReconciler) validateHTTPRouteRefs(ctx context.Context, refs []corev1.LocalObjectReference, llmSvc *v1alpha1.LLMInferenceService, validation *RefValidationResult) []*gatewayapi.HTTPRoute {
	referencedRoutes := make([]*gatewayapi.HTTPRoute, 0)

	for _, routeRef := range refs {
		providedRoute := &gatewayapi.HTTPRoute{}
		errGet := r.Client.Get(ctx, types.NamespacedName{
			Namespace: llmSvc.GetNamespace(),
			Name:      routeRef.Name,
		}, providedRoute)
		if errGet != nil {
			if apierrors.IsNotFound(errGet) {
				validation.AddNotFoundIssue(fmt.Sprintf("HTTPRoute %s/%s does not exist", llmSvc.GetNamespace(), routeRef.Name))
			}

			continue
		}
		referencedRoutes = append(referencedRoutes, providedRoute)
	}

	return referencedRoutes
}

// validateHTTPRouteConfigs validates that referenced HTTPRoutes are properly configured
func (r *LLMInferenceServiceReconciler) validateHTTPRouteConfigs(ctx context.Context, referencedRoutes []*gatewayapi.HTTPRoute, llmSvc *v1alpha1.LLMInferenceService, validation *RefValidationResult) {
	if len(referencedRoutes) == 0 {
		return
	}

	expectedBackendName := llmSvc.InferencePoolName()

	for _, route := range referencedRoutes {
		hasCorrectBackend := false
		for _, rule := range route.Spec.Rules {
			for _, backendRef := range rule.BackendRefs {
				backendName := string(backendRef.BackendRef.BackendObjectReference.Name)
				if backendName == expectedBackendName {
					hasCorrectBackend = true
					break
				}
			}
			if hasCorrectBackend {
				break
			}
		}

		if !hasCorrectBackend {
			validation.AddMisconfiguredIssue(fmt.Sprintf("HTTPRoute %s/%s does not target service %s", route.Namespace, route.Name, expectedBackendName))
		}

		parentGateways := extractParentRefGateways(route.Spec.ParentRefs, route.Namespace)
		r.validateGatewayRefs(ctx, parentGateways, validation)
	}
}

func extractParentRefGateways(parentRefs []gatewayapi.ParentReference, defaultNamespace string) []types.NamespacedName {
	gatewayRefs := make([]types.NamespacedName, 0)

	for _, ref := range parentRefs {
		gatewayRefs = append(gatewayRefs, types.NamespacedName{
			Name:      string(ref.Name),
			Namespace: string(ptr.Deref(ref.Namespace, gatewayapi.Namespace(defaultNamespace))),
		})
	}
	return gatewayRefs
}

func (r *LLMInferenceServiceReconciler) validateGatewayRefs(ctx context.Context, gatewayRefs []types.NamespacedName, validation *RefValidationResult) {
	for _, gatewayRef := range gatewayRefs {
		if message := r.validateGatewayRef(ctx, gatewayRef); message != "" {
			validation.AddNotFoundIssue(message)
		}
	}
}

func (r *LLMInferenceServiceReconciler) validateGatewayRef(ctx context.Context, namespacedName types.NamespacedName) string {
	refGateway := &gatewayapi.Gateway{}
	errGet := r.Client.Get(ctx, namespacedName, refGateway)
	if apierrors.IsNotFound(errGet) {
		return fmt.Sprintf("Gateway %s/%s does not exist", namespacedName.Namespace, namespacedName.Name)
	}
	// We ignore other errors as they might be transient and should be handled by retry logic
	return ""
}
