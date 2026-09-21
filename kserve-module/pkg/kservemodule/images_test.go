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

func TestWVAImageParamMap_AllValuesAreRelatedImage(t *testing.T) {
	g := NewWithT(t)
	for key, val := range wvaImageParamMap {
		g.Expect(val).Should(HavePrefix("RELATED_IMAGE_"), "key %q has value %q without RELATED_IMAGE_ prefix", key, val)
	}
}

// applyParams skips keys absent from params.env without error, so a renamed
// key would ship the bundle's default image unnoticed.
func TestModelExpressImageParamMap_MatchesBundleParams(t *testing.T) {
	g := NewWithT(t)
	g.Expect(modelExpressImageParamMap).Should(Equal(map[string]string{
		"MODELEXPRESS_OPERATOR_IMAGE": "RELATED_IMAGE_ODH_MODELEXPRESS_OPERATOR_IMAGE",
		"MODELEXPRESS_SERVER_IMAGE":   "RELATED_IMAGE_ODH_MODELEXPRESS_IMAGE",
	}))
}

func TestImageParamMaps_NoKeyOverlap(t *testing.T) {
	g := NewWithT(t)
	for key := range kserveImageParamMap {
		_, exists := modelControllerImageParamMap[key]
		g.Expect(exists).Should(BeFalse(), "key %q exists in both kserve and modelcontroller image maps", key)
	}
}
