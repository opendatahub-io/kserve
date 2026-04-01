//go:build !distro

/*
Copyright 2021 The KServe Authors.

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

package ingress

import (
	"github.com/kserve/kserve/pkg/utils"
)

// filterIngressAnnotations filters annotations against the disallowed list.
// In upstream builds annotations are always filtered using the full disallowed list.
func filterIngressAnnotations(annotations map[string]string, disallowedList []string) map[string]string {
	return utils.Filter(annotations, func(key string) bool {
		return !utils.Includes(disallowedList, key)
	})
}
