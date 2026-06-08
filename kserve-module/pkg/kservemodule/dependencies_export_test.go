package kservemodule

type CRDInfo struct {
	Name     string // Full CRD name (e.g. "certificates.cert-manager.io")
	Group    string // Extracted group (e.g. "cert-manager.io")
	Resource string // Extracted plural resource (e.g. "certificates")
}

func parseCRDName(crdName string) CRDInfo {
	// CRD names follow <plural>.<group> convention.
	idx := 0
	for i, c := range crdName {
		if c == '.' {
			idx = i
			break
		}
	}
	return CRDInfo{
		Name:     crdName,
		Group:    crdName[idx+1:],
		Resource: crdName[:idx],
	}
}

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
		if dep.checkType == checkCRD && dep.critical {
			result = append(result, parseCRDName(dep.crdName))
		}
	}
	return result
}
