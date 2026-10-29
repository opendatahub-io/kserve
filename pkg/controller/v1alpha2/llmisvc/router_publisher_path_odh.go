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
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

// normalizePublisherPathMatches removes boundary slashes from the model name in
// publisher path matches. The route preset interpolates the name after /models/;
// boundary slashes would make the resulting HTTPRoute path invalid. Keep model
// routing headers unchanged because they identify the model by its declared name.
func normalizePublisherPathMatches(llmSvc *v1alpha2.LLMInferenceService, route *gwapiv1.HTTPRoute) {
	modelName := resolveModelName(llmSvc)
	cleanName := strings.Trim(modelName, "/")
	if cleanName == modelName {
		return
	}

	rawPrefix := "/" + fullyQualifiedModelName(llmSvc.Namespace, modelName)
	if cleanName == "" {
		// No model segment remains, so a publisher catch-all would claim
		// every model path in the namespace. Keep the other access methods.
		route.Spec.Rules = stripPerModelPathMatches(route.Spec.Rules, []string{rawPrefix}, "")
		return
	}
	cleanPrefix := normalizedPublisherPath(llmSvc.Namespace, cleanName)

	for i := range route.Spec.Rules {
		for j := range route.Spec.Rules[i].Matches {
			path := route.Spec.Rules[i].Matches[j].Path
			if path == nil || path.Value == nil ||
				(path.Type != nil && *path.Type != gwapiv1.PathMatchExact && *path.Type != gwapiv1.PathMatchPathPrefix) {
				continue
			}
			if value := *path.Value; value == rawPrefix || strings.HasPrefix(value, rawPrefix+"/") {
				*path.Value = cleanPrefix + strings.TrimPrefix(value, rawPrefix)
			}
		}
	}
}

func normalizedPublisherPath(namespace, modelName string) string {
	path := "/publishers/" + namespace + "/models"
	if name := strings.Trim(modelName, "/"); name != "" {
		path += "/" + name
	}
	return path
}

// warnPublisherPathCollisions reports model names that are distinct for model
// routing but resolve to the same publisher path after slash normalization.
func (r *LLMISVCReconciler) warnPublisherPathCollisions(llmSvc *v1alpha2.LLMInferenceService, route *gwapiv1.HTTPRoute, others []v1alpha2.LLMInferenceService) {
	collisions := findPublisherPathCollisions(llmSvc, route, others)
	if len(collisions) == 0 {
		return
	}
	r.Eventf(llmSvc, corev1.EventTypeWarning, "PublisherPathCollision",
		"publisher path %q for model %q overlaps with %s in namespace %s after trimming model-name slashes; "+
			"the gateway may shadow one service. Reachable at its own address %s/%s",
		normalizedPublisherPath(llmSvc.Namespace, resolveModelName(llmSvc)), resolveModelName(llmSvc),
		strings.Join(collisions, ", "), llmSvc.Namespace, llmSvc.Namespace, llmSvc.Name)
}

func findPublisherPathCollisions(self *v1alpha2.LLMInferenceService, route *gwapiv1.HTTPRoute, others []v1alpha2.LLMInferenceService) []string {
	path := normalizedPublisherPath(self.Namespace, resolveModelName(self))
	if !hasPublisherPathMatch(route, path) {
		return nil
	}

	selfModelNames := modelRoutingNames(self)
	var collisions []string
	for i := range others {
		other := &others[i]
		if other.Namespace != self.Namespace || other.Name == self.Name ||
			other.DeletionTimestamp != nil || sameRoutingGroup(self, other) ||
			otherModelNamesOverlap(selfModelNames, other) {
			continue
		}
		if peerPublishesPath(other, path) {
			collisions = append(collisions, other.Name)
		}
	}
	slices.Sort(collisions)
	return collisions
}

func hasPublisherPathMatch(route *gwapiv1.HTTPRoute, prefix string) bool {
	for _, rule := range route.Spec.Rules {
		for _, match := range rule.Matches {
			if len(match.Headers) > 0 || match.Path == nil || match.Path.Value == nil ||
				(match.Path.Type != nil && *match.Path.Type != gwapiv1.PathMatchExact && *match.Path.Type != gwapiv1.PathMatchPathPrefix) {
				continue
			}
			value := *match.Path.Value
			if value == prefix || strings.HasPrefix(value, prefix+"/") {
				return true
			}
		}
	}
	return false
}

func peerPublishesPath(peer *v1alpha2.LLMInferenceService, path string) bool {
	for _, address := range peer.Status.Addresses {
		if address.URL != nil && address.URL.Path == path {
			return true
		}
	}
	if len(peer.Status.Addresses) > 0 {
		return false
	}
	if peer.Spec.Router != nil && peer.Spec.Router.Route != nil && peer.Spec.Router.Route.HTTP.HasRefs() {
		return false
	}
	return normalizedPublisherPath(peer.Namespace, resolveModelName(peer)) == path
}
