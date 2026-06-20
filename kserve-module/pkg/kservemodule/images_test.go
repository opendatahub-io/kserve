package kservemodule

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestKserveImageParamMap_AllValuesAreRelatedImage(t *testing.T) {
	g := NewWithT(t)
	for key, val := range kserveImageParamMap {
		g.Expect(val).Should(HavePrefix("RELATED_IMAGE_"), "key %q has value %q without RELATED_IMAGE_ prefix", key, val)
	}
}

func TestModelControllerImageParamMap_AllValuesAreRelatedImage(t *testing.T) {
	g := NewWithT(t)
	for key, val := range modelControllerImageParamMap {
		g.Expect(val).Should(HavePrefix("RELATED_IMAGE_"), "key %q has value %q without RELATED_IMAGE_ prefix", key, val)
	}
}

func TestWvaImageParamMap_AllValuesAreRelatedImage(t *testing.T) {
	g := NewWithT(t)
	for key, val := range wvaImageParamMap {
		g.Expect(val).Should(HavePrefix("RELATED_IMAGE_"), "key %q has value %q without RELATED_IMAGE_ prefix", key, val)
	}
}

func TestImageParamMaps_NoKeyOverlap(t *testing.T) {
	g := NewWithT(t)
	for key := range kserveImageParamMap {
		_, exists := modelControllerImageParamMap[key]
		g.Expect(exists).Should(BeFalse(), "key %q exists in both kserve and modelcontroller image maps", key)
		_, exists = wvaImageParamMap[key]
		g.Expect(exists).Should(BeFalse(), "key %q exists in both kserve and wva image maps", key)
	}
	for key := range modelControllerImageParamMap {
		_, exists := wvaImageParamMap[key]
		g.Expect(exists).Should(BeFalse(), "key %q exists in both modelcontroller and wva image maps", key)
	}
}
