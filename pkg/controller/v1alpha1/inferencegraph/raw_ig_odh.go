//go:build distro

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

package inferencegraph

import (
	"context"
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	distrotls "github.com/kserve/kserve/pkg/tls/distro"
)

// routerTLSConfig is the subset of the inferenceservice-config "router" document
// that selects the RawDeployment InferenceGraph listener. A nil EnableTLS keeps
// TLS enabled.
type routerTLSConfig struct {
	EnableTLS *bool `json:"enableTLS,omitempty"`
}

// customizeRouterPodSpec serves the router over TLS with the effective cluster
// TLS policy, unless security.opendatahub.io/enable-tls or router.enableTLS
// disables it. The router still trusts the OpenShift service CA so it can call
// HTTPS nodes, and when auth is enabled it runs with the auth-verifier
// ServiceAccount. TLS args, profile args and CA bundle entries are prepended
// and the auth args appended: reordering args, env, volume mounts or volumes
// changes the pod template and rolls router pods.
func customizeRouterPodSpec(ctx context.Context, reader client.Reader, graph *v1alpha1.InferenceGraph, podSpec *corev1.PodSpec) error {
	if podSpec == nil || len(podSpec.Containers) == 0 {
		return nil
	}

	configured, err := readRouterTLSConfig(ctx, reader)
	if err != nil {
		return err
	}

	router := &podSpec.Containers[0]
	if routerTLSEnabled(graph, configured) {
		router.Args = append([]string{"--enable-tls"}, router.Args...)
		// Fail closed: a router serving against an unresolvable profile policy is
		// worse than failing this reconciliation and surfacing the error.
		if err := distrotls.InjectGoTLSArgs(ctx, reader, router); err != nil {
			return err
		}
		if router.ReadinessProbe != nil && router.ReadinessProbe.HTTPGet != nil {
			router.ReadinessProbe.HTTPGet.Scheme = corev1.URISchemeHTTPS
		}
	}
	addServiceCaBundle(podSpec)
	podSpec.ServiceAccountName = "default"

	// If auth is enabled for the InferenceGraph:
	// * Add --enable-auth argument, to properly secure kserve-router
	// * Add the --inferencegraph-name argument, so that the router is aware of its name
	// * Enable auto-mount of the ServiceAccount, because it is required for validating tokens
	// * Set a non-default ServiceAccount with enough privileges to verify auth
	if graph.GetAnnotations()[constants.ODHKserveRawAuth] == "true" {
		router.Args = append(router.Args, "--enable-auth")

		router.Args = append(router.Args, "--inferencegraph-name")
		router.Args = append(router.Args, graph.GetName())

		podSpec.AutomountServiceAccountToken = proto.Bool(true)

		// In ODH, when auth is enabled, it is required to have the InferenceGraph running
		// with a ServiceAccount that can query the Kubernetes API to validate tokens
		// and privileges.
		// In KServe v0.14 there is no way for users to set the ServiceAccount for an
		// InferenceGraph. In ODH this is used at our advantage to set a non-default SA
		// and bind needed privileges for the auth verification.
		podSpec.ServiceAccountName = graph.GetName() + "-auth-verifier"
	}

	return nil
}

// routerTLSEnabled decides whether the RawDeployment router listens with TLS.
// An explicit "true" or "false" annotation wins. Any other annotation value is
// ignored. With no usable annotation, router.enableTLS in inferenceservice-config
// applies, and a missing field keeps TLS enabled.
func routerTLSEnabled(graph *v1alpha1.InferenceGraph, configured *bool) bool {
	if graph != nil {
		if value, ok := graph.GetAnnotations()[constants.ODHKserveEnableTLS]; ok {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "false":
				return false
			case "true":
				return true
			}
		}
	}
	if configured != nil {
		return *configured
	}
	return true
}

// readRouterTLSConfig reads router.enableTLS from inferenceservice-config.
// A missing ConfigMap, or a client that cannot read ConfigMaps, means the
// field is unset. A present document that does not parse is an error.
func readRouterTLSConfig(ctx context.Context, reader client.Reader) (*bool, error) {
	if reader == nil {
		return nil, nil
	}

	cm := &corev1.ConfigMap{}
	err := reader.Get(ctx, types.NamespacedName{
		Name:      constants.InferenceServiceConfigMapName,
		Namespace: constants.KServeNamespace,
	}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) {
			return nil, nil
		}
		return nil, err
	}

	raw := strings.TrimSpace(cm.Data["router"])
	if raw == "" {
		return nil, nil
	}
	parsed := &routerTLSConfig{}
	if err := json.Unmarshal([]byte(raw), parsed); err != nil {
		return nil, err
	}
	return parsed.EnableTLS, nil
}

// addServiceCaBundle mounts the OpenShift service CA bundle into the router container and points
// SSL_CERT_FILE at it, so the router trusts serving certificates issued by the service CA.
// The entries are prepended, so SSL_CERT_FILE stays ahead of PROPAGATE_HEADERS.
func addServiceCaBundle(podSpec *corev1.PodSpec) {
	if len(podSpec.Containers) == 0 {
		return
	}

	router := &podSpec.Containers[0]
	router.VolumeMounts = append([]corev1.VolumeMount{
		{
			Name:      constants.ServiceCaBundleVolumeName,
			MountPath: constants.ServiceCaBundleMountPath,
		},
	}, router.VolumeMounts...)
	router.Env = append([]corev1.EnvVar{
		{
			Name:  "SSL_CERT_FILE",
			Value: constants.ServiceCaBundleMountPath + "/" + constants.ServiceCaBundleCertFile,
		},
	}, router.Env...)
	podSpec.Volumes = append([]corev1.Volume{
		{
			Name: constants.ServiceCaBundleVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: constants.OpenShiftServiceCaConfigMapName,
					},
				},
			},
		},
	}, podSpec.Volumes...)
}
