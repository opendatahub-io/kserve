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

package testing

import (
	"errors"
	"fmt"

	"k8s.io/utils/ptr"

	"github.com/onsi/gomega/types"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1"
)

// extractHTTPRoute safely extracts HTTPRoute from either pointer or value type
func extractHTTPRoute(actual any) (*gatewayapi.HTTPRoute, error) {
	switch v := actual.(type) {
	case *gatewayapi.HTTPRoute:
		if v == nil {
			return nil, errors.New("expected non-nil *gatewayapi.HTTPRoute, but got nil")
		}
		return v, nil
	case gatewayapi.HTTPRoute:
		return &v, nil
	default:
		return nil, fmt.Errorf("expected *gatewayapi.HTTPRoute or gatewayapi.HTTPRoute, but got %T", actual)
	}
}

// HaveGatewayRefs returns a matcher that checks if an HTTPRoute has the specified gateway parent refs
func HaveGatewayRefs(expectedGatewayNames ...string) types.GomegaMatcher {
	return &haveGatewayRefsMatcher{
		expectedGatewayNames: expectedGatewayNames,
	}
}

type haveGatewayRefsMatcher struct {
	expectedGatewayNames []string
	actualParentRefs     []gatewayapi.ParentReference
	actualGatewayNames   []string
}

func (matcher *haveGatewayRefsMatcher) Match(actual any) (success bool, err error) {
	httpRoute, err := extractHTTPRoute(actual)
	if err != nil {
		return false, err
	}

	matcher.actualParentRefs = httpRoute.Spec.ParentRefs

	actualNames := make([]string, 0, len(matcher.actualParentRefs))
	for _, ref := range matcher.actualParentRefs {
		if ptr.Deref(ref.Kind, "") == "Gateway" {
			actualNames = append(actualNames, string(ref.Name))
		}
	}
	matcher.actualGatewayNames = actualNames

	if len(actualNames) != len(matcher.expectedGatewayNames) {
		return false, nil
	}

	expectedSet := make(map[string]bool)
	for _, name := range matcher.expectedGatewayNames {
		expectedSet[name] = true
	}

	for _, name := range actualNames {
		if !expectedSet[name] {
			return false, nil
		}
	}

	return true, nil
}

func (matcher *haveGatewayRefsMatcher) FailureMessage(actual any) string {
	return fmt.Sprintf("Expected %T to have gateway refs %v, but found %v",
		actual, matcher.expectedGatewayNames, matcher.actualGatewayNames)
}

func (matcher *haveGatewayRefsMatcher) NegatedFailureMessage(actual any) string {
	return fmt.Sprintf("Expected %T to not have gateway refs %v, but they were found",
		actual, matcher.expectedGatewayNames)
}

// HaveBackendRefs returns a matcher that checks if an HTTPRoute has the specified backend refs
func HaveBackendRefs(expectedBackendNames ...string) types.GomegaMatcher {
	return &haveBackendRefsMatcher{
		expectedBackendNames: expectedBackendNames,
	}
}

type haveBackendRefsMatcher struct {
	expectedBackendNames []string
	actualBackendNames   []string
}

func (matcher *haveBackendRefsMatcher) Match(actual any) (success bool, err error) {
	httpRoute, err := extractHTTPRoute(actual)
	if err != nil {
		return false, err
	}

	var backendNames []string
	for _, rule := range httpRoute.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			backendNames = append(backendNames, string(backendRef.Name))
		}
	}
	matcher.actualBackendNames = backendNames

	if len(backendNames) != len(matcher.expectedBackendNames) {
		return false, nil
	}

	expectedSet := make(map[string]bool)
	for _, name := range matcher.expectedBackendNames {
		expectedSet[name] = true
	}

	for _, name := range backendNames {
		if !expectedSet[name] {
			return false, nil
		}
	}

	return true, nil
}

func (matcher *haveBackendRefsMatcher) FailureMessage(actual any) string {
	return fmt.Sprintf("Expected %T to have backend refs %v, but found %v",
		actual, matcher.expectedBackendNames, matcher.actualBackendNames)
}

func (matcher *haveBackendRefsMatcher) NegatedFailureMessage(actual any) string {
	return fmt.Sprintf("Expected %T to not have backend refs %v, but they were found",
		actual, matcher.expectedBackendNames)
}

// HaveGatewayRefsInNamespace returns a matcher that checks if an HTTPRoute has the specified gateway parent refs in the given namespace
func HaveGatewayRefsInNamespace(namespace string, expectedGatewayNames ...string) types.GomegaMatcher {
	return &haveGatewayRefsInNamespaceMatcher{
		expectedNamespace:    namespace,
		expectedGatewayNames: expectedGatewayNames,
	}
}

type haveGatewayRefsInNamespaceMatcher struct {
	expectedNamespace    string
	expectedGatewayNames []string
	actualParentRefs     []gatewayapi.ParentReference
	actualGatewayNames   []string
}

func (matcher *haveGatewayRefsInNamespaceMatcher) Match(actual any) (success bool, err error) {
	httpRoute, err := extractHTTPRoute(actual)
	if err != nil {
		return false, err
	}

	matcher.actualParentRefs = httpRoute.Spec.ParentRefs

	actualNames := make([]string, 0, len(matcher.actualParentRefs))
	for _, ref := range matcher.actualParentRefs {
		// Only consider Gateway kind refs in the specified namespace
		if ptr.Deref(ref.Kind, "") == "Gateway" && ptr.Deref(ref.Namespace, "") == gatewayapi.Namespace(matcher.expectedNamespace) {
			actualNames = append(actualNames, string(ref.Name))
		}
	}
	matcher.actualGatewayNames = actualNames

	if len(actualNames) != len(matcher.expectedGatewayNames) {
		return false, nil
	}

	expectedSet := make(map[string]bool)
	for _, name := range matcher.expectedGatewayNames {
		expectedSet[name] = true
	}

	for _, name := range actualNames {
		if !expectedSet[name] {
			return false, nil
		}
	}

	return true, nil
}

func (matcher *haveGatewayRefsInNamespaceMatcher) FailureMessage(actual any) string {
	return fmt.Sprintf("Expected %T to have gateway refs %v in namespace %q, but found %v",
		actual, matcher.expectedGatewayNames, matcher.expectedNamespace, matcher.actualGatewayNames)
}

func (matcher *haveGatewayRefsInNamespaceMatcher) NegatedFailureMessage(actual any) string {
	return fmt.Sprintf("Expected %T to not have gateway refs %v in namespace %q, but they were found",
		actual, matcher.expectedGatewayNames, matcher.expectedNamespace)
}
