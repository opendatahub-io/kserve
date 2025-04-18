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

// Code generated by lister-gen. DO NOT EDIT.

package v1alpha1

import (
	v1alpha1 "github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/listers"
	"k8s.io/client-go/tools/cache"
)

// TrainedModelLister helps list TrainedModels.
// All objects returned here must be treated as read-only.
type TrainedModelLister interface {
	// List lists all TrainedModels in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.TrainedModel, err error)
	// TrainedModels returns an object that can list and get TrainedModels.
	TrainedModels(namespace string) TrainedModelNamespaceLister
	TrainedModelListerExpansion
}

// trainedModelLister implements the TrainedModelLister interface.
type trainedModelLister struct {
	listers.ResourceIndexer[*v1alpha1.TrainedModel]
}

// NewTrainedModelLister returns a new TrainedModelLister.
func NewTrainedModelLister(indexer cache.Indexer) TrainedModelLister {
	return &trainedModelLister{listers.New[*v1alpha1.TrainedModel](indexer, v1alpha1.Resource("trainedmodel"))}
}

// TrainedModels returns an object that can list and get TrainedModels.
func (s *trainedModelLister) TrainedModels(namespace string) TrainedModelNamespaceLister {
	return trainedModelNamespaceLister{listers.NewNamespaced[*v1alpha1.TrainedModel](s.ResourceIndexer, namespace)}
}

// TrainedModelNamespaceLister helps list and get TrainedModels.
// All objects returned here must be treated as read-only.
type TrainedModelNamespaceLister interface {
	// List lists all TrainedModels in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.TrainedModel, err error)
	// Get retrieves the TrainedModel from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1alpha1.TrainedModel, error)
	TrainedModelNamespaceListerExpansion
}

// trainedModelNamespaceLister implements the TrainedModelNamespaceLister
// interface.
type trainedModelNamespaceLister struct {
	listers.ResourceIndexer[*v1alpha1.TrainedModel]
}
