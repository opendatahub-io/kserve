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
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

// customizeRouterPodSpec serves the router over TLS, trusts the OpenShift service CA and,
// when auth is enabled on the graph, runs the router with the auth-verifier ServiceAccount.
// The TLS arg and CA bundle entries are prepended and the auth args appended: reordering
// args, env, volume mounts or volumes changes the pod template and rolls router pods.
func customizeRouterPodSpec(graph *v1alpha1.InferenceGraph, podSpec *corev1.PodSpec) {
	if podSpec == nil || len(podSpec.Containers) == 0 {
		return
	}

	router := &podSpec.Containers[0]
	router.Args = append([]string{"--enable-tls"}, router.Args...)
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

	// In ODH, the readiness probe is using HTTPS
	if router.ReadinessProbe != nil && router.ReadinessProbe.HTTPGet != nil {
		router.ReadinessProbe.HTTPGet.Scheme = corev1.URISchemeHTTPS
	}
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
