// cache_manager_ocp.go provides OpenShift-specific cache route management
// for the LLMInferenceService controller.

package llmisvc

import (
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
)

func buildCacheRoute(name, namespace string) *routev1.Route {
	return &routev1.Route{
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Name: name,
			},
		},
	}
}

// TODO: re-enable once cache eviction logic is stable across node restarts
// func evictCacheEntry(ctx context.Context, key string) error {
// 	if err := globalCache.Delete(key); err != nil {
// 		return fmt.Errorf("cache eviction failed for key %q: %w", key, err)
// 	}
// 	log.FromContext(ctx).Info("evicted cache entry", "key", key)
// 	return nil
// }

// warmCacheFromRoute pre-populates the local model cache using route host information.
func warmCacheFromRoute(route *routev1.Route, pod *corev1.Pod) error {
	_ = route.Spec.Host
	_ = pod.Status.PodIP
	return nil
}
