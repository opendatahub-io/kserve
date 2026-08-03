package kservemodule

import (
	"fmt"
	"strings"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
)

type CRDInfo struct {
	Name     string // Full CRD name (e.g. "certificates.cert-manager.io")
	Group    string // Extracted group (e.g. "cert-manager.io")
	Resource string // Extracted plural resource (e.g. "certificates")
}

func parseCRDName(crdName string) CRDInfo {
	parts := strings.SplitN(crdName, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		panic(fmt.Sprintf("invalid CRD name format (expected <plural>.<group>): %q", crdName))
	}
	return CRDInfo{
		Name:     crdName,
		Group:    parts[1],
		Resource: parts[0],
	}
}

var (
	ConditionLLMISVCDeps       = conditionLLMISVCDeps
	ConditionLLMISVCWideEPDeps = conditionLLMISVCWideEPDeps
	ConditionLLMDWVADeps       = conditionLLMDWVADeps
)

func XKSCRDDependenciesForTest() []CRDInfo {
	var result []CRDInfo
	for _, dep := range allDependencies {
		if dep.checkType == checkCRD && dep.platform == "xks" {
			result = append(result, parseCRDName(dep.crdName))
		}
	}
	return result
}

func CriticalCRDDependenciesForTest() []CRDInfo {
	var result []CRDInfo
	for _, dep := range allDependencies {
		if dep.checkType == checkCRD && dep.availabilitySeverity == common.ConditionSeverityError {
			result = append(result, parseCRDName(dep.crdName))
		}
	}
	return result
}
