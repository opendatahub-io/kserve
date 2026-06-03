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

const tlsVolumeName = "proxy-tls"

func isInferenceGraph(componentMeta metav1.ObjectMeta) bool {
	_, ok := componentMeta.Labels[constants.InferenceGraphLabel]
	return ok
}

// sarVolumeNameForDeployment returns the volume name to use for the SAR ConfigMap.
// If an existing deployment already has a working legacy volume name (isvcName-kube-rbac-proxy-sar-config),
// it is preserved to avoid triggering an unnecessary deployment rollout during upgrades.
// For new deployments, it returns the fixed constant to stay within the 63-character limit.
func sarVolumeNameForDeployment(isvcName string, existingDeployment *appsv1.Deployment) string {
	if existingDeployment != nil {
		for _, v := range existingDeployment.Spec.Template.Spec.Volumes {
			if v.Name == constants.OauthProxySARCMName {
				return constants.OauthProxySARCMName
			}
		}
	}
	return fmt.Sprintf("%s-%s", isvcName, constants.OauthProxySARCMName)
}

func buildDeployments(ctx context.Context,
	client kclient.Client,
	componentMeta metav1.ObjectMeta,
	workerComponentMeta metav1.ObjectMeta,
	componentExt *v1beta1.ComponentExtensionSpec,
	podSpec *corev1.PodSpec, workerPodSpec *corev1.PodSpec,
	deployConfig *v1beta1.DeployConfig,
) ([]*appsv1.Deployment, bool, error) {
	deploymentList, err := createRawDeployment(componentMeta, workerComponentMeta, componentExt, podSpec, workerPodSpec, deployConfig)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create raw deployment: %w", err)
	}

	for _, d := range deploymentList {
		if d.Spec.Template.Annotations == nil {
			d.Spec.Template.Annotations = make(map[string]string)
		}
		d.Spec.Template.Annotations[constants.OpenshiftServingCertAnnotation] = d.Name + constants.ServingCertSecretSuffix
	}

	var isvcname string
	if val, ok := componentMeta.Labels[constants.InferenceServicePodLabelKey]; ok {
		isvcname = val
	} else {
		isvcname = componentMeta.Name
	}

	isIG := isInferenceGraph(componentMeta)

	existingProxyType, existingProxyImage, existingDeployment, err := getExistingAuthProxyType(ctx, client,
		componentMeta.Namespace, componentMeta.Name)
	if err != nil {
		return nil, false, fmt.Errorf("failed to fetch deployment %s/%s: %w", componentMeta.Namespace, componentMeta.Name, err)
	}
	existingDeploymentFound := existingDeployment != nil

	sarVolumeName := sarVolumeNameForDeployment(isvcname, existingDeployment)

	// shouldAddAuthProxy controls whether the OAuth proxy sidecar is injected or preserved.
	// For InferenceService: always inject for new deployments (to avoid pod-template rollouts
	// when auth is later toggled), preserve for existing deployments that already carry the
	// proxy, and also inject when auth is explicitly enabled via annotation.
	shouldAddAuthProxy := false
	if !isIG {
		if !existingDeploymentFound {
			shouldAddAuthProxy = true
		} else {
			if val, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
				shouldAddAuthProxy = true
			}
			for _, c := range existingDeployment.Spec.Template.Spec.Containers {
				if c.Name == constants.KubeRbacContainerName || c.Name == constants.OauthProxyContainerName {
					shouldAddAuthProxy = true
					break
				}
			}
		}
	}

	headDeployment := deploymentList[0]

	authProxyPreserved := false
	if shouldAddAuthProxy {
		wantsMigration := false
		if val, ok := componentMeta.Annotations[constants.ODHAuthProxyTypeAnnotation]; ok {
			wantsMigration = val == constants.KubeRbacProxyType
		}

		oauthConfig, cfgErr := getOauthProxyConfig(ctx, client)
		if cfgErr != nil {
			log.Error(cfgErr, "Failed to load oauthProxy config, proxy injection may fail")
			oauthConfig = nil
		}

		if existingProxyType != "" {
			switch existingProxyType {
			case constants.OauthProxyContainerName:
				if wantsMigration {
					err := addOauthContainerToDeployment(ctx, client, oauthConfig, headDeployment, componentMeta, componentExt, podSpec, isvcname, sarVolumeName)
					if err != nil {
						return nil, false, err
					}
				} else {
					log.Info("Preserving existing auth proxy container", "isvc", isvcname, "type", existingProxyType)
					authProxyPreserved = true
					copyAuthProxyFromExisting(existingDeployment, headDeployment, existingProxyType)
				}
			case constants.KubeRbacContainerName:
				configuredKubeRbacImage := ""
				if oauthConfig != nil {
					configuredKubeRbacImage = oauthConfig.Image
				}
				if configuredKubeRbacImage != "" && existingProxyImage == configuredKubeRbacImage {
					err := addOauthContainerToDeployment(ctx, client, oauthConfig, headDeployment, componentMeta, componentExt, podSpec, isvcname, sarVolumeName)
					if err != nil {
						return nil, false, err
					}
				} else {
					log.Info("Preserving existing auth proxy container (image differs from config)",
						"isvc", isvcname, "type", existingProxyType,
						"existingImage", existingProxyImage, "configImage", configuredKubeRbacImage)
					authProxyPreserved = true
					copyAuthProxyFromExisting(existingDeployment, headDeployment, existingProxyType)
				}
			}
		} else {
			err := addOauthContainerToDeployment(ctx, client, oauthConfig, headDeployment, componentMeta, componentExt, podSpec, isvcname, sarVolumeName)
			if err != nil {
				return nil, false, err
			}
		}
	}
	if (shouldAddAuthProxy && !authProxyPreserved) || isIG {
		mountServingSecretCMVolumeToDeployment(headDeployment, componentMeta, isIG, isvcname, sarVolumeName)
	}
	return deploymentList, authProxyPreserved, nil
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

func mountServingSecretCMVolumeToDeployment(deployment *appsv1.Deployment, componentMeta metav1.ObjectMeta, inferenceGraph bool, isvcName string, sarVolumeName string) {
	updatedPodSpec := deployment.Spec.Template.Spec.DeepCopy()
	tlsSecretVolume := corev1.Volume{
		Name: tlsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  componentMeta.Name + constants.ServingCertSecretSuffix,
				DefaultMode: ptr.To[int32](420),
			},
		},
	}

	kubeRbacProxyConfigVolume := corev1.Volume{
		Name: sarVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: fmt.Sprintf("%s-%s", isvcName, constants.OauthProxySARCMName),
				},
				DefaultMode: ptr.To[int32](420),
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
	oauthConfig *v1beta1.OauthConfig,
	deployment *appsv1.Deployment,
	componentMeta metav1.ObjectMeta,
	componentExt *v1beta1.ComponentExtensionSpec,
	podSpec *corev1.PodSpec, isvcName string, sarVolumeName string,
) error {
	var upstreamPort, upstreamTimeout string

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

	oauthProxyContainer, err := generateOauthProxyContainer(ctx, client, oauthConfig, isvcName, componentMeta.Namespace, upstreamPort, upstreamTimeout, sarVolumeName)
	if err != nil {
		return err
	}
	updatedPodSpec := deployment.Spec.Template.Spec.DeepCopy()
	// ODH override. See: https://issues.redhat.com/browse/RHOAIENG-19904
	updatedPodSpec.AutomountServiceAccountToken = proto.Bool(true)
	updatedPodSpec.Containers = append(updatedPodSpec.Containers, *oauthProxyContainer)
	deployment.Spec.Template.Spec = *updatedPodSpec
	return nil
}

func generateOauthProxyContainer(ctx context.Context, client kclient.Client,
	oauthConfig *v1beta1.OauthConfig, isvc string, namespace string, upstreamPort string, upstreamTimeout string,
	sarVolumeName string,
) (*corev1.Container, error) {
	if err := createSarCm(ctx, client, namespace, isvc); err != nil {
		return nil, fmt.Errorf("failed to create SAR configmap: %w", err)
	}

	if oauthConfig == nil {
		return nil, errors.New("oauthProxy config is nil")
	}
	if oauthConfig.Image == "" || oauthConfig.MemoryRequest == "" || oauthConfig.MemoryLimit == "" ||
		oauthConfig.CpuRequest == "" || oauthConfig.CpuLimit == "" {
		return nil, errors.New("one or more required oauthProxyConfig fields are empty")
	}
	oauthImage := oauthConfig.Image
	oauthMemoryRequest := oauthConfig.MemoryRequest
	oauthMemoryLimit := oauthConfig.MemoryLimit
	oauthCpuRequest := oauthConfig.CpuRequest
	oauthCpuLimit := oauthConfig.CpuLimit
	oauthUpstreamTimeout := strings.TrimSpace(oauthConfig.UpstreamTimeoutSeconds)
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
		`--config-file=/etc/kube-rbac-proxy/config-file.yaml`,
		`--v=4`,
	}
	if oauthUpstreamTimeout != "" {
		if _, err := strconv.ParseInt(oauthUpstreamTimeout, 10, 64); err != nil {
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
				corev1.ResourceCPU:    resource.MustParse(oauthCpuLimit),
				corev1.ResourceMemory: resource.MustParse(oauthMemoryLimit),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(oauthCpuRequest),
				corev1.ResourceMemory: resource.MustParse(oauthMemoryRequest),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      tlsVolumeName,
				MountPath: "/etc/tls/private",
			},
			{
				Name:      sarVolumeName,
				MountPath: "/etc/kube-rbac-proxy",
				ReadOnly:  true,
			},
		},
	}, nil
}

func createSarCm(ctx context.Context, client kclient.Client, namespace string, inferenceServiceName string) error {
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
					APIVersion:         v1beta1.SchemeGroupVersion.String(),
					Kind:               "InferenceService",
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

func copyAuthProxyFromExisting(existing, desired *appsv1.Deployment, containerName string) {
	if existing == nil || desired == nil {
		return
	}

	existingSpec := &existing.Spec.Template.Spec
	desiredSpec := &desired.Spec.Template.Spec

	var authProxyContainer *corev1.Container
	for i, c := range existingSpec.Containers {
		if c.Name == containerName {
			authProxyContainer = &existingSpec.Containers[i]
			break
		}
	}
	if authProxyContainer == nil {
		return
	}

	desiredSpec.Containers = append(desiredSpec.Containers, *authProxyContainer)
	desiredSpec.AutomountServiceAccountToken = existingSpec.AutomountServiceAccountToken

	authVolumeNames := make(map[string]bool, len(authProxyContainer.VolumeMounts))
	for _, vm := range authProxyContainer.VolumeMounts {
		authVolumeNames[vm.Name] = true
	}

	for _, v := range existingSpec.Volumes {
		if authVolumeNames[v.Name] {
			desiredSpec.Volumes = append(desiredSpec.Volumes, v)
		}
	}

	for i, c := range desiredSpec.Containers {
		if c.Name == constants.InferenceServiceContainerName {
			for _, existingC := range existingSpec.Containers {
				if existingC.Name == constants.InferenceServiceContainerName {
					for _, vm := range existingC.VolumeMounts {
						if authVolumeNames[vm.Name] {
							desiredSpec.Containers[i].VolumeMounts = append(
								desiredSpec.Containers[i].VolumeMounts, vm)
						}
					}
					break
				}
			}
			break
		}
	}
}

func getExistingAuthProxyType(ctx context.Context, client kclient.Client,
	namespace, deploymentName string,
) (containerName string, containerImage string, existing *appsv1.Deployment, err error) {
	existing = &appsv1.Deployment{}
	err = client.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      deploymentName,
	}, existing)

	if apierr.IsNotFound(err) {
		return "", "", nil, nil
	}
	if err != nil {
		return "", "", nil, err
	}

	for _, container := range existing.Spec.Template.Spec.Containers {
		if container.Name == constants.OauthProxyContainerName {
			return constants.OauthProxyContainerName, container.Image, existing, nil
		}
		if container.Name == constants.KubeRbacContainerName {
			return constants.KubeRbacContainerName, container.Image, existing, nil
		}
	}
	return "", "", existing, nil
}

func getOauthProxyConfig(ctx context.Context, client kclient.Client) (*v1beta1.OauthConfig, error) {
	isvcConfigMap := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, isvcConfigMap); err != nil {
		return nil, err
	}
	oauthProxyJSON := strings.TrimSpace(isvcConfigMap.Data["oauthProxy"])
	oauthProxyConfig := &v1beta1.OauthConfig{}
	if err := json.Unmarshal([]byte(oauthProxyJSON), oauthProxyConfig); err != nil {
		return nil, err
	}
	return oauthProxyConfig, nil
}
