package kservemodule

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func filterFastResources(resources []unstructured.Unstructured) []unstructured.Unstructured {
	type resourceKey struct {
		gvk  schema.GroupVersionKind
		name string
	}

	byKey := make(map[resourceKey]int, len(resources))
	for i, r := range resources {
		byKey[resourceKey{r.GroupVersionKind(), r.GetName()}] = i
	}

	exclude := make(map[int]bool)

	for i, r := range resources {
		baseName, _, isFast := parseFastSuffix(r.GetName())
		if !isFast {
			continue
		}

		fastImage := extractImage(r)
		if fastImage == "" {
			continue
		}

		stableKey := resourceKey{r.GroupVersionKind(), baseName}
		if stableIdx, ok := byKey[stableKey]; ok {
			stableImage := extractImage(resources[stableIdx])
			if stableImage != "" && fastImage == stableImage {
				exclude[i] = true
			}
		}
	}

	for i, r := range resources {
		if exclude[i] {
			continue
		}
		baseName, suffix, isFast := parseFastSuffix(r.GetName())
		if !isFast || suffix != "-fast-1" {
			continue
		}

		fast2Key := resourceKey{r.GroupVersionKind(), baseName + "-fast-2"}
		fast2Idx, ok := byKey[fast2Key]
		if !ok || exclude[fast2Idx] {
			continue
		}

		fast1Image := extractImage(r)
		fast2Image := extractImage(resources[fast2Idx])
		if fast1Image != "" && fast1Image == fast2Image {
			exclude[i] = true
		}
	}

	result := make([]unstructured.Unstructured, 0, len(resources)-len(exclude))
	for i, r := range resources {
		if !exclude[i] {
			result = append(result, r)
		}
	}
	return result
}

func parseFastSuffix(name string) (baseName, suffix string, isFast bool) {
	if strings.HasSuffix(name, "-fast-1") {
		return strings.TrimSuffix(name, "-fast-1"), "-fast-1", true
	}
	if strings.HasSuffix(name, "-fast-2") {
		return strings.TrimSuffix(name, "-fast-2"), "-fast-2", true
	}
	return name, "", false
}

func extractImage(r unstructured.Unstructured) string {
	gvk := r.GroupVersionKind()

	switch {
	case gvk.Group == llmISVCConfigGroup && gvk.Kind == llmISVCConfigKind:
		return findContainerImage(r.Object, "main", "spec", "template", "containers")
	case gvk.Group == templateGroup && gvk.Kind == templateKind:
		objects, found, err := unstructured.NestedSlice(r.Object, "objects")
		if err != nil || !found || len(objects) == 0 {
			return ""
		}
		obj, ok := objects[0].(map[string]any)
		if !ok {
			return ""
		}
		return findContainerImage(obj, "kserve-container", "spec", "containers")
	default:
		return ""
	}
}

func findContainerImage(obj map[string]any, containerName string, path ...string) string {
	containers, found, err := unstructured.NestedSlice(obj, path...)
	if err != nil || !found {
		return ""
	}
	for _, c := range containers {
		container, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(container, "name")
		if name == containerName {
			image, _, _ := unstructured.NestedString(container, "image")
			return image
		}
	}
	return ""
}
