/*
Copyright 2026 The KServe Authors.

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

package validation

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/kserve/kserve/pkg/constants"
)

// ModelExpressMode selects how ModelExpress takes part in loading model weights.
type ModelExpressMode string

const (
	// ModelExpressModeLayered keeps KServe's model delivery and adds peer-to-peer
	// weight transfer on top.
	ModelExpressModeLayered ModelExpressMode = "layered"
	// ModelExpressModeNative skips KServe's weight download and lets ModelExpress
	// load the weights.
	ModelExpressModeNative ModelExpressMode = "native"
)

var modelExpressModes = []string{string(ModelExpressModeLayered), string(ModelExpressModeNative)}

var modelExpressOptionKeys = []string{
	constants.ModelExpressAddressAnnotationKey,
	constants.ModelExpressTokenAudienceAnnotationKey,
	constants.ModelExpressRevisionAnnotationKey,
	constants.ModelExpressCABundleConfigMapAnnotationKey,
	constants.ModelExpressCABundleKeyAnnotationKey,
}

// ModelExpressModeFromAnnotations returns the requested ModelExpress mode, or ""
// when ModelExpress is not enabled.
func ModelExpressModeFromAnnotations(annotations map[string]string) (ModelExpressMode, error) {
	raw, ok := annotations[constants.ModelExpressModeAnnotationKey]
	if !ok {
		return "", nil
	}
	switch mode := ModelExpressMode(strings.TrimSpace(raw)); mode {
	case ModelExpressModeLayered, ModelExpressModeNative:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid %s value %q: must be one of %s",
			constants.ModelExpressModeAnnotationKey, raw, strings.Join(modelExpressModes, ", "))
	}
}

// ValidateModelExpressAddress accepts host:port, http://host:port and https://host:port.
func ValidateModelExpressAddress(address string) error {
	hostPort := address
	if scheme, rest, found := strings.Cut(address, "://"); found {
		if scheme != "http" && scheme != "https" {
			return fmt.Errorf("unsupported scheme %q: use host:port, http://host:port or https://host:port", scheme)
		}
		hostPort = rest
	}
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}
	if host == "" {
		return errors.New("host must not be empty")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %q must be a number between 1 and 65535", port)
	}
	return nil
}

// ModelExpressAddressUsesTLS reports whether the client connects to address over TLS.
func ModelExpressAddressUsesTLS(address string) bool {
	return strings.HasPrefix(address, "https://")
}

// ValidateModelExpressCABundleAnnotations validates the annotations naming the
// ConfigMap and key that hold the CA bundle for an https:// server.
func ValidateModelExpressCABundleAnnotations(annotations map[string]string) field.ErrorList {
	var allErrs field.ErrorList
	annotationsPath := field.NewPath("metadata").Child("annotations")
	configMapPath := annotationsPath.Key(constants.ModelExpressCABundleConfigMapAnnotationKey)
	keyPath := annotationsPath.Key(constants.ModelExpressCABundleKeyAnnotationKey)

	rawConfigMap, hasConfigMap := annotations[constants.ModelExpressCABundleConfigMapAnnotationKey]
	rawKey, hasKey := annotations[constants.ModelExpressCABundleKeyAnnotationKey]
	if !hasConfigMap {
		if hasKey {
			allErrs = append(allErrs, field.Required(configMapPath, fmt.Sprintf("%s is required when %s is set",
				constants.ModelExpressCABundleConfigMapAnnotationKey, constants.ModelExpressCABundleKeyAnnotationKey)))
		}
		return allErrs
	}

	if errs := validation.IsDNS1123Subdomain(strings.TrimSpace(rawConfigMap)); len(errs) > 0 {
		allErrs = append(allErrs, field.Invalid(configMapPath, rawConfigMap, "must be a ConfigMap name: "+strings.Join(errs, "; ")))
	}
	if hasKey {
		if errs := validation.IsConfigMapKey(strings.TrimSpace(rawKey)); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(keyPath, rawKey, "must be a ConfigMap key: "+strings.Join(errs, "; ")))
		}
	}
	if address, ok := annotations[constants.ModelExpressAddressAnnotationKey]; ok && !ModelExpressAddressUsesTLS(strings.TrimSpace(address)) {
		allErrs = append(allErrs, field.Invalid(configMapPath, rawConfigMap,
			fmt.Sprintf("a CA bundle requires an https:// %s, got %q", constants.ModelExpressAddressAnnotationKey, address)))
	}
	return allErrs
}

// ValidateModelExpressAnnotations validates ModelExpress annotations on any resource.
func ValidateModelExpressAnnotations(annotations map[string]string) field.ErrorList {
	var allErrs field.ErrorList
	if len(annotations) == 0 {
		return allErrs
	}
	annotationsPath := field.NewPath("metadata").Child("annotations")

	if _, enabled := annotations[constants.ModelExpressModeAnnotationKey]; !enabled {
		for _, key := range modelExpressOptionKeys {
			if _, ok := annotations[key]; ok {
				allErrs = append(allErrs, field.Required(
					annotationsPath.Key(constants.ModelExpressModeAnnotationKey),
					fmt.Sprintf("%s is required when %s is set", constants.ModelExpressModeAnnotationKey, key),
				))
			}
		}
		return allErrs
	}

	if _, err := ModelExpressModeFromAnnotations(annotations); err != nil {
		allErrs = append(allErrs, field.NotSupported(
			annotationsPath.Key(constants.ModelExpressModeAnnotationKey),
			annotations[constants.ModelExpressModeAnnotationKey],
			modelExpressModes,
		))
	}

	if raw, ok := annotations[constants.ModelExpressAddressAnnotationKey]; ok {
		if err := ValidateModelExpressAddress(strings.TrimSpace(raw)); err != nil {
			allErrs = append(allErrs, field.Invalid(
				annotationsPath.Key(constants.ModelExpressAddressAnnotationKey), raw, err.Error()))
		}
	}

	for _, key := range []string{constants.ModelExpressTokenAudienceAnnotationKey, constants.ModelExpressRevisionAnnotationKey} {
		if raw, ok := annotations[key]; ok && strings.TrimSpace(raw) == "" {
			allErrs = append(allErrs, field.Invalid(annotationsPath.Key(key), raw, "must not be empty"))
		}
	}

	allErrs = append(allErrs, ValidateModelExpressCABundleAnnotations(annotations)...)

	return allErrs
}

// ValidateModelExpressSource reports whether mode can load the model at modelURI
// together with LoRA adapters at loraURIs. Native mode accepts s3:// models with
// pvc:// adapters only, and hf:// models with any adapters.
func ValidateModelExpressSource(mode ModelExpressMode, modelURI string, loraURIs []string) error {
	if mode != ModelExpressModeNative {
		return nil
	}
	switch {
	case strings.HasPrefix(modelURI, constants.HfURIPrefix):
		return nil
	case strings.HasPrefix(modelURI, constants.S3URIPrefix):
		for _, uri := range loraURIs {
			if !strings.HasPrefix(uri, constants.PvcURIPrefix) {
				return fmt.Errorf("%s=%s with an s3:// model supports only pvc:// LoRA adapters, got %q",
					constants.ModelExpressModeAnnotationKey, ModelExpressModeNative, uri)
			}
		}
		return nil
	default:
		return fmt.Errorf("%s=%s supports s3:// and hf:// models, got %q; use %s for other sources",
			constants.ModelExpressModeAnnotationKey, ModelExpressModeNative, modelURI, ModelExpressModeLayered)
	}
}

// ValidateModelExpress validates the ModelExpress annotations and, when modelURI
// is set, that the requested mode can load it with the given LoRA adapters.
func ValidateModelExpress(annotations map[string]string, modelURI string, loraURIs []string) field.ErrorList {
	allErrs := ValidateModelExpressAnnotations(annotations)
	if len(allErrs) > 0 {
		return allErrs
	}
	mode, _ := ModelExpressModeFromAnnotations(annotations)
	if mode == "" || modelURI == "" {
		return allErrs
	}
	if err := ValidateModelExpressSource(mode, modelURI, loraURIs); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "model", "uri"), modelURI, err.Error()))
	}
	return allErrs
}
