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

package testing

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kserve/kserve/pkg/constants"
)

// HardwareProfile builds a HardwareProfile unstructured object for testing.
// It avoids importing opendatahub-operator types by using the unstructured API.
//
// Parameters:
//   - name: HardwareProfile name
//   - namespace: HardwareProfile namespace
//   - spec: HardwareProfile spec fields (identifiers, schedulingSpec, etc.)
func HardwareProfile(name, namespace string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": constants.HardwareProfileGroup + "/" + constants.HardwareProfileVersion,
			"kind":       "HardwareProfile",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
}

// HWPResourceSpec returns a HardwareProfile spec with resource identifiers.
// Each identifier is a []string{resourceName, defaultCount}.
//
// Parameters:
//   - identifiers: list of (resourceName, defaultCount) pairs
func HWPResourceSpec(identifiers ...[]string) map[string]interface{} {
	items := make([]interface{}, 0, len(identifiers))
	for _, id := range identifiers {
		item := map[string]interface{}{
			"identifier": id[0],
		}
		if len(id) > 1 && id[1] != "" {
			item["defaultCount"] = id[1]
		}
		items = append(items, item)
	}
	return map[string]interface{}{
		"identifiers": items,
	}
}

// HWPNodeSpec returns a HardwareProfile spec with node scheduling.
//
// Parameters:
//   - nodeSelector: key-value pairs for node selection
//   - tolerations: list of toleration maps
func HWPNodeSpec(nodeSelector map[string]interface{}, tolerations []interface{}) map[string]interface{} {
	node := map[string]interface{}{}
	if nodeSelector != nil {
		node["nodeSelector"] = nodeSelector
	}
	if tolerations != nil {
		node["tolerations"] = tolerations
	}
	return map[string]interface{}{
		"schedulingSpec": map[string]interface{}{
			"type": "Node",
			"node": node,
		},
	}
}

// HWPKueueSpec returns a HardwareProfile spec with Kueue queue scheduling.
//
// Parameters:
//   - localQueueName: Kueue local queue name
func HWPKueueSpec(localQueueName string) map[string]interface{} {
	return map[string]interface{}{
		"schedulingSpec": map[string]interface{}{
			"type": "Queue",
			"kueue": map[string]interface{}{
				"localQueueName": localQueueName,
			},
		},
	}
}
