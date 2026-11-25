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

package llmisvc

import (
	"context"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

// postReconcilePVCEvictors extends shared-evictor reconciliation after resource
// creation/cleanup, including partial failures, before routing. It returns the
// existing stage error unchanged when no platform work is needed.
func (r *LLMISVCReconciler) postReconcilePVCEvictors(_ context.Context, _ *v1alpha2.LLMInferenceService, evictorErr error) error {
	return evictorErr
}
