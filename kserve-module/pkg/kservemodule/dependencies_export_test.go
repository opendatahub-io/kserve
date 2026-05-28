package kservemodule

import "k8s.io/apimachinery/pkg/runtime/schema"

func XKSCRDDependenciesForTest() []schema.GroupKind {
	var result []schema.GroupKind
	for _, dep := range allDependencies {
		if dep.checkType == checkCRD && dep.platform == "xks" {
			result = append(result, dep.groupKind)
		}
	}
	return result
}

func CriticalCRDDependenciesForTest() []schema.GroupKind {
	var result []schema.GroupKind
	for _, dep := range allDependencies {
		if dep.checkType == checkCRD && dep.critical {
			result = append(result, dep.groupKind)
		}
	}
	return result
}
