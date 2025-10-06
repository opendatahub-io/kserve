/*
Copyright 2023 The KServe Authors.

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

package v1alpha1

// This has been extracted as v1 does not utilise criticality, but v1alpha2 does.

// ModelCriticality expresses the relative importance of serving a model.
// This is used by our scheduler integration and stays in KServe domain.
type ModelCriticality string

const (
	// Critical traffic must not be dropped when possible.
	Critical ModelCriticality = "Critical"

	// Sheddable traffic may be dropped under load.
	Sheddable ModelCriticality = "Sheddable"
)
