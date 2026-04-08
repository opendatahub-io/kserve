//go:build distro

/*
Copyright 2021 The KServe Authors.

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

package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const (
	tlsVolumeName = "proxy-tls"
)

// isInferenceGraph reports whether the component being reconciled belongs to an InferenceGraph
// rather than an InferenceService.
//
// We derive the answer from constants.InferenceGraphLabel, which raw_ig.go always stamps on
// componentMeta before construction, so the information is already in scope and no API call
// is needed. A label check is preferable to a client.Get here for three reasons:
//  1. No informer coupling: an API call requires v1alpha1.InferenceGraph to be in the
//     manager's watch list; if the controllers are ever split into separate binaries,
//     that assumption silently breaks.
//  2. No error suppression: a Get can fail transiently, and returning false on error would
//     silently skip OAuth proxy injection for a graph component.
//  3. Consistency: service_reconciler.go makes the same distinction using the same label.
func isInferenceGraph(componentMeta metav1.ObjectMeta) bool {
	_, ok := componentMeta.Labels[constants.InferenceGraphLabel]
	return ok
}

func buildDeployments(ctx context.Context,
	client kclient.Client,
	componentMeta metav1.ObjectMeta,
	workerComponentMeta metav1.ObjectMeta,
	componentExt *v1beta1.ComponentExtensionSpec,
	podSpec *corev1.PodSpec, workerPodSpec *corev1.PodSpec,
	deployConfig *v1beta1.DeployConfig,
) ([]*appsv1.Deployment, error) {
	deploymentList, err := createRawDeployment(componentMeta, workerComponentMeta, componentExt, podSpec, workerPodSpec, deployConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create raw deployment: %w", err)
	}

	// Add OpenShift serving cert annotation to all pod templates so the serving-cert controller
	// creates the TLS secret that the pods (and OAuth proxy) will mount.
	for _, d := range deploymentList {
		if d.Spec.Template.Annotations == nil {
			d.Spec.Template.Annotations = make(map[string]string)
		}
		d.Spec.Template.Annotations[constants.OpenshiftServingCertAnnotation] = d.Name + constants.ServingCertSecretSuffix
	}

	// get the Inference Service Name
	var isvcname string
	if val, ok := componentMeta.Labels[constants.InferenceServicePodLabelKey]; ok {
		isvcname = val
	} else {
		isvcname = componentMeta.Name
	}

	isIG := isInferenceGraph(componentMeta)
	enableAuth := false
	// Deployment list is for multi-node, we only need to add oauth proxy and serving cert volumes to the head deployment
	headDeployment := deploymentList[0]
	if val, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		enableAuth = true

		if !isIG { // InferenceGraphs don't use rbac-proxy
			if err := addOauthContainerToDeployment(ctx, client, headDeployment, componentMeta, componentExt, podSpec, isvcname); err != nil {
				return nil, err
			}
		}
	}
	if enableAuth || isIG {
		mountServingSecretCMVolumeToDeployment(headDeployment, componentMeta, isIG, isvcname)
	}
	return deploymentList, nil
}

func getKServeContainerPort(podSpec *corev1.PodSpec) string {
	var kserveContainerPort string

	for _, container := range podSpec.Containers {
		if container.Name == "transformer-container" {
			if len(container.Ports) > 0 {
				return strconv.Itoa(int(container.Ports[0].ContainerPort))
			}
		}
		if container.Name == "kserve-container" {
			if len(container.Ports) > 0 {
				kserveContainerPort = strconv.Itoa(int(container.Ports[0].ContainerPort))
			}
		}
	}

	return kserveContainerPort
}

func mountServingSecretCMVolumeToDeployment(deployment *appsv1.Deployment, componentMeta metav1.ObjectMeta, inferenceGraph bool, isvcName string) {
	updatedPodSpec := deployment.Spec.Template.Spec.DeepCopy()
	tlsSecretVolume := corev1.Volume{
		Name: tlsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  componentMeta.Name + constants.ServingCertSecretSuffix,
				DefaultMode: func(i int32) *int32 { return &i }(420),
			},
		},
	}

	kubeRbacProxyConfigVolume := corev1.Volume{
		Name: fmt.Sprintf("%s-%s", isvcName, constants.OauthProxySARCMName),
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: fmt.Sprintf("%s-%s", isvcName, constants.OauthProxySARCMName),
				},
				DefaultMode: func(i int32) *int32 { return &i }(420),
			},
		},
	}

	updatedPodSpec.Volumes = append(updatedPodSpec.Volumes, tlsSecretVolume, kubeRbacProxyConfigVolume)

	containerName := "kserve-container"
	if inferenceGraph {
		containerName = componentMeta.Name
	}
	for i, container := range updatedPodSpec.Containers {
		if container.Name == containerName {
			updatedPodSpec.Containers[i].VolumeMounts = append(updatedPodSpec.Containers[i].VolumeMounts, corev1.VolumeMount{
				Name:      tlsVolumeName,
				MountPath: "/etc/tls/private",
			})
		}
	}

	deployment.Spec.Template.Spec = *updatedPodSpec
}

func addOauthContainerToDeployment(ctx context.Context,
	client kclient.Client,
	deployment *appsv1.Deployment,
	componentMeta metav1.ObjectMeta,
	componentExt *v1beta1.ComponentExtensionSpec,
	podSpec *corev1.PodSpec, isvcName string,
) error {
	var upstreamPort, upstreamTimeout string

	if val, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		switch {
		case componentExt != nil && componentExt.Batcher != nil:
			upstreamPort = constants.InferenceServiceDefaultAgentPortStr
		case componentExt != nil && componentExt.Logger != nil:
			upstreamPort = constants.InferenceServiceDefaultAgentPortStr
		default:
			upstreamPort = getKServeContainerPort(podSpec)
			if upstreamPort == "" {
				upstreamPort = constants.InferenceServiceDefaultHttpPort
			}
		}

		if componentExt != nil && componentExt.TimeoutSeconds != nil {
			upstreamTimeout = strconv.FormatInt(*componentExt.TimeoutSeconds, 10)
		}

		oauthProxyContainer, err := generateOauthProxyContainer(ctx, client, isvcName, componentMeta.Namespace, upstreamPort, upstreamTimeout)
		if err != nil {
			return err
		}
		updatedPodSpec := deployment.Spec.Template.Spec.DeepCopy()
		// ODH override. See : https://issues.redhat.com/browse/RHOAIENG-19904
		updatedPodSpec.AutomountServiceAccountToken = proto.Bool(true)
		updatedPodSpec.Containers = append(updatedPodSpec.Containers, *oauthProxyContainer)
		deployment.Spec.Template.Spec = *updatedPodSpec
	}
	return nil
}

func generateOauthProxyContainer(ctx context.Context, client kclient.Client, isvc string,
	namespace string, upstreamPort string, upstreamTimeout string,
) (*corev1.Container, error) {
	// Create SAR ConfigMap for this specific InferenceService
	if err := createSarCm(ctx, client, namespace, isvc); err != nil {
		return nil, fmt.Errorf("failed to create SAR configmap: %w", err)
	}

	isvcConfigMap := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, isvcConfigMap); err != nil {
		return nil, err
	}
	oauthProxyJSON := strings.TrimSpace(isvcConfigMap.Data["oauthProxy"])
	oauthProxyConfig := v1beta1.OauthConfig{}
	if err := json.Unmarshal([]byte(oauthProxyJSON), &oauthProxyConfig); err != nil {
		return nil, err
	}
	if oauthProxyConfig.Image == "" || oauthProxyConfig.MemoryRequest == "" || oauthProxyConfig.MemoryLimit == "" ||
		oauthProxyConfig.CpuRequest == "" || oauthProxyConfig.CpuLimit == "" {
		return nil, errors.New("one or more required oauthProxyConfig fields are empty")
	}
	oauthImage := oauthProxyConfig.Image
	oauthUpstreamTimeout := strings.TrimSpace(oauthProxyConfig.UpstreamTimeoutSeconds)

	cpuLimit, err := resource.ParseQuantity(oauthProxyConfig.CpuLimit)
	if err != nil {
		return nil, fmt.Errorf("invalid oauthProxy config cpuLimit value %q: %w", oauthProxyConfig.CpuLimit, err)
	}
	memoryLimit, err := resource.ParseQuantity(oauthProxyConfig.MemoryLimit)
	if err != nil {
		return nil, fmt.Errorf("invalid oauthProxy config memoryLimit value %q: %w", oauthProxyConfig.MemoryLimit, err)
	}
	cpuRequest, err := resource.ParseQuantity(oauthProxyConfig.CpuRequest)
	if err != nil {
		return nil, fmt.Errorf("invalid oauthProxy config cpuRequest value %q: %w", oauthProxyConfig.CpuRequest, err)
	}
	memoryRequest, err := resource.ParseQuantity(oauthProxyConfig.MemoryRequest)
	if err != nil {
		return nil, fmt.Errorf("invalid oauthProxy config memoryRequest value %q: %w", oauthProxyConfig.MemoryRequest, err)
	}
	if upstreamTimeout != "" {
		oauthUpstreamTimeout = upstreamTimeout
	}

	args := []string{
		`--secure-listen-address=:` + strconv.Itoa(constants.OauthProxyPort),
		`--proxy-endpoints-port=8643`,
		`--upstream=http://localhost:` + upstreamPort,
		`--auth-header-fields-enabled=true`,
		`--tls-cert-file=/etc/tls/private/tls.crt`,
		`--tls-private-key-file=/etc/tls/private/tls.key`,
		// Defines the SAR
		`--config-file=/etc/kube-rbac-proxy/config-file.yaml`,
		`--v=4`,
	}
	if oauthUpstreamTimeout != "" {
		if _, err = strconv.ParseInt(oauthUpstreamTimeout, 10, 64); err != nil {
			return nil, fmt.Errorf("invalid oauthProxy config upstreamTimeoutSeconds value %q: %w", oauthUpstreamTimeout, err)
		}
		args = append(args, `--upstream-timeout=`+oauthUpstreamTimeout+`s`)
	}

	return &corev1.Container{
		Name:  constants.KubeRbacContainerName,
		Args:  args,
		Image: oauthImage,
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: constants.OauthProxyPort,
				Name:          "https",
			},
			{
				ContainerPort: constants.OauthProxyProbePort,
				Name:          "proxy",
			},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/healthz",
					Port:   intstr.FromInt32(constants.OauthProxyProbePort),
					Scheme: corev1.URISchemeHTTPS,
				},
			},
			InitialDelaySeconds: 30,
			TimeoutSeconds:      1,
			PeriodSeconds:       5,
			SuccessThreshold:    1,
			FailureThreshold:    3,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/healthz",
					Port:   intstr.FromInt32(constants.OauthProxyProbePort),
					Scheme: corev1.URISchemeHTTPS,
				},
			},
			InitialDelaySeconds: 5,
			TimeoutSeconds:      1,
			PeriodSeconds:       5,
			SuccessThreshold:    1,
			FailureThreshold:    3,
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    cpuLimit,
				corev1.ResourceMemory: memoryLimit,
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    cpuRequest,
				corev1.ResourceMemory: memoryRequest,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      tlsVolumeName,
				MountPath: "/etc/tls/private",
			},
			{
				Name:      fmt.Sprintf("%s-%s", isvc, constants.OauthProxySARCMName),
				MountPath: "/etc/kube-rbac-proxy",
				ReadOnly:  true,
			},
		},
	}, nil
}

// createSarCm creates or updates a ConfigMap containing SAR (SubjectAccessReview) configuration
// for the kube-rbac-proxy container. This configmap defines the authorization parameters
// for accessing the specific InferenceService.
func createSarCm(ctx context.Context, client kclient.Client, namespace string, inferenceServiceName string) error {
	// Get the InferenceService to obtain its UID for owner reference
	inferenceService := &v1beta1.InferenceService{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: inferenceServiceName}, inferenceService); err != nil {
		return fmt.Errorf("failed to get InferenceService for owner reference: %w", err)
	}

	configMapName := fmt.Sprintf("%s-%s", inferenceServiceName, constants.OauthProxySARCMName)
	configContent := fmt.Sprintf(`authorization:
  resourceAttributes:
    namespace: "%s"
    apiGroup: "serving.kserve.io"
    apiVersion: "v1beta1"
    resource: "inferenceservices"
    name: "%s"
    verb: "get"`, namespace, inferenceServiceName)

	sarConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         inferenceService.APIVersion,
					Kind:               inferenceService.Kind,
					Name:               inferenceService.Name,
					UID:                inferenceService.UID,
					Controller:         ptr.To(true),
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Data: map[string]string{
			"config-file.yaml": configContent,
		},
		Immutable: ptr.To(true),
	}

	existingConfigMap := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: configMapName}, existingConfigMap); err != nil {
		if !apierr.IsNotFound(err) {
			return fmt.Errorf("failed to get SAR configmap: %w", err)
		}
		if err = client.Create(ctx, sarConfigMap); err != nil {
			return fmt.Errorf("failed to create SAR configmap: %w", err)
		}
		log.V(2).Info("Created SAR ConfigMap", "name", configMapName, "namespace", namespace)
		return nil
	}

	// Since ConfigMap is immutable, if content differs we need to delete and recreate
	if existingConfigMap.Data["config-file.yaml"] != configContent {
		log.V(2).Info("SAR ConfigMap - changes detected, will be recreated", "name", configMapName, "namespace", namespace)
		if err := client.Delete(ctx, existingConfigMap); err != nil {
			return fmt.Errorf("failed to delete existing SAR configmap: %w", err)
		}
		log.V(2).Info("Deleted existing SAR ConfigMap", "name", configMapName, "namespace", namespace)
		if err := client.Create(ctx, sarConfigMap); err != nil {
			return fmt.Errorf("failed to recreate SAR configmap: %w", err)
		}
		log.V(2).Info("Recreated SAR ConfigMap", "name", configMapName, "namespace", namespace)
	}
	return nil
}
