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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	servingv1beta1 "github.com/kserve/kserve/pkg/client/clientset/versioned/typed/serving/v1beta1"
	gentype "k8s.io/client-go/gentype"
)

// fakeInferenceServices implements InferenceServiceInterface
type fakeInferenceServices struct {
	*gentype.FakeClientWithList[*v1beta1.InferenceService, *v1beta1.InferenceServiceList]
	Fake *FakeServingV1beta1
}

func newFakeInferenceServices(fake *FakeServingV1beta1, namespace string) servingv1beta1.InferenceServiceInterface {
	return &fakeInferenceServices{
		gentype.NewFakeClientWithList[*v1beta1.InferenceService, *v1beta1.InferenceServiceList](
			fake.Fake,
			namespace,
			v1beta1.SchemeGroupVersion.WithResource("inferenceservices"),
			v1beta1.SchemeGroupVersion.WithKind("InferenceService"),
			func() *v1beta1.InferenceService { return &v1beta1.InferenceService{} },
			func() *v1beta1.InferenceServiceList { return &v1beta1.InferenceServiceList{} },
			func(dst, src *v1beta1.InferenceServiceList) { dst.ListMeta = src.ListMeta },
			func(list *v1beta1.InferenceServiceList) []*v1beta1.InferenceService {
				return gentype.ToPointerSlice(list.Items)
			},
			func(list *v1beta1.InferenceServiceList, items []*v1beta1.InferenceService) {
				list.Items = gentype.FromPointerSlice(items)
			},
		),
		fake,
	}
}
