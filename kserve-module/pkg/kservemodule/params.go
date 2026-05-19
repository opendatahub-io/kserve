// [TEMPORARY] Adapted from opendatahub-operator (pkg/deploy/envParams.go).
// TODO: migrate to odh-platform-utilities once ApplyParams is moved to the shared library.
// Remove this comment once migrated.
package kservemodule

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func parseParams(fileName string) (map[string]string, error) {
	paramsEnv, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer paramsEnv.Close()

	paramsEnvMap := make(map[string]string)
	scanner := bufio.NewScanner(paramsEnv)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			paramsEnvMap[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return paramsEnvMap, nil
}

func writeParamsEnv(params map[string]string, dir string) (string, error) {
	tmp, err := os.CreateTemp(dir, "params.env-")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		tmp.Close()
		if !success {
			_ = os.Remove(tmp.Name())
		}
	}()

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	writer := bufio.NewWriter(tmp)
	for _, key := range keys {
		if _, err := fmt.Fprintf(writer, "%s=%s\n", key, params[key]); err != nil {
			return "", err
		}
	}
	if err := writer.Flush(); err != nil {
		return "", fmt.Errorf("failed to write to file: %w", err)
	}

	success = true
	return tmp.Name(), nil
}

func applyParams(componentPath string, imageParamsMap map[string]string, extraParamsMaps ...map[string]string) error {
	paramsFile := filepath.Join(componentPath, "params.env")

	paramsEnvMap, err := parseParams(paramsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	updated := false
	for key := range paramsEnvMap {
		envVar := imageParamsMap[key]
		if envVar == "" {
			continue
		}
		envVal := os.Getenv(envVar)
		if envVal != "" && envVal != paramsEnvMap[key] {
			paramsEnvMap[key] = envVal
			updated = true
		}
	}

	for _, extraMap := range extraParamsMaps {
		for key, value := range extraMap {
			if paramsEnvMap[key] != value {
				paramsEnvMap[key] = value
				updated = true
			}
		}
	}

	if !updated {
		return nil
	}

	tmp, err := writeParamsEnv(paramsEnvMap, componentPath)
	if err != nil {
		return err
	}

	if err = os.Rename(tmp, paramsFile); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed rename %s to %s: %w", tmp, paramsFile, err)
	}

	return nil
}
