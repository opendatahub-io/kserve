//go:build distro

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

package components

import (
	"github.com/kserve/kserve/pkg/constants"
	isvcutils "github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/utils"
	"github.com/kserve/kserve/pkg/utils"
)

// filterServiceAnnotations filters annotations against the disallowed list.
// In Standard (Knative) mode the ODHKserveRawAuth annotation is also disallowed;
// in RawDeployment mode it is allowed through so the auth proxy can be configured.
// https://issues.redhat.com/browse/RHOAIENG-20326
func filterServiceAnnotations(annotations map[string]string, disallowedList []string, deploymentMode constants.DeploymentModeType) map[string]string {
	if deploymentMode == constants.Standard {
		return utils.Filter(annotations, func(key string) bool {
			return !utils.Includes(isvcutils.FilterList(disallowedList, constants.ODHKserveRawAuth), key)
		})
	}
	return utils.Filter(annotations, func(key string) bool {
		return !utils.Includes(disallowedList, key)
	})
}
