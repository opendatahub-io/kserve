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

// FilterList returns the items of slice other than element. The distro annotation filters use it
// to let the auth annotation through the disallowed list.
func FilterList(slice []string, element string) []string {
	var result []string
	for _, item := range slice {
		if item != element {
			result = append(result, item)
		}
	}
	return result
}
