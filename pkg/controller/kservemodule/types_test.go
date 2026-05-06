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

package kservemodule

import (
	"testing"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/kserve/kserve/pkg/apis/platform/v1alpha1"

	. "github.com/onsi/gomega"
)

func TestGetManagementState_FromSpec(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	kserve.Spec.ManagementState = common.Removed

	g.Expect(platformv1alpha1.GetManagementState(kserve)).Should(Equal(common.Removed))
}

func TestGetManagementState_DefaultManaged(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}

	g.Expect(platformv1alpha1.GetManagementState(kserve)).Should(Equal(common.Managed))
}

func TestGetManagementState_Managed(t *testing.T) {
	g := NewWithT(t)

	kserve := &platformv1alpha1.Kserve{}
	kserve.Spec.ManagementState = common.Managed

	g.Expect(platformv1alpha1.GetManagementState(kserve)).Should(Equal(common.Managed))
}
