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

package utils

import "context"

type inferenceServiceReconcileKey struct{}

// WithInferenceServiceReconcile returns a copy of ctx marked as an
// InferenceService reconcile. The raw Deployment platform hook also runs for
// InferenceGraph routers, and component labels can't tell the two apart: they
// include user-set metadata, which may carry either kind's label.
func WithInferenceServiceReconcile(ctx context.Context) context.Context {
	return context.WithValue(ctx, inferenceServiceReconcileKey{}, true)
}

// IsInferenceServiceReconcile reports whether ctx was marked by
// WithInferenceServiceReconcile.
func IsInferenceServiceReconcile(ctx context.Context) bool {
	marked, _ := ctx.Value(inferenceServiceReconcileKey{}).(bool)
	return marked
}
