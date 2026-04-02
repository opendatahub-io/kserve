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
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const (
	tlsVolumeName = "proxy-tls"
)

func buildDeployments(ctx context.Context,
	client kclient.Client,
	clientset kubernetes.Interface,
	resourceType constants.ResourceType,
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

	// get the Inference Service Name
	var isvcname string
	if val, ok := componentMeta.Labels[constants.InferenceServicePodLabelKey]; ok {
		isvcname = val
	} else {
		isvcname = componentMeta.Name
	}

	enableAuth := false
	// Deployment list is for multi-node, we only need to add oauth proxy and serving sercret certs to the head deployment
	headDeployment := deploymentList[0]
	if val, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]; ok && strings.EqualFold(val, "true") {
		enableAuth = true

		if resourceType != constants.InferenceGraphResource { // InferenceGraphs don't use rbac-proxy
			err := addOauthContainerToDeployment(ctx, client, clientset, headDeployment, componentMeta, componentExt, podSpec, isvcname)
			if err != nil {
				return nil, err
			}
		}
	}
	if (resourceType == constants.InferenceServiceResource && enableAuth) || resourceType == constants.InferenceGraphResource {
		mountServingSecretCMVolumeToDeployment(headDeployment, componentMeta, resourceType, isvcname)
	}
	return deploymentList, nil
}

func mountServingSecretCMVolumeToDeployment(deployment *appsv1.Deployment, componentMeta metav1.ObjectMeta, resourceType constants.ResourceType, isvcName string) {
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
	if resourceType == constants.InferenceGraphResource {
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
	clientset kubernetes.Interface,
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
			upstreamPort = GetKServeContainerPort(podSpec)
			if upstreamPort == "" {
				upstreamPort = constants.InferenceServiceDefaultHttpPort
			}
		}

		if componentExt != nil && componentExt.TimeoutSeconds != nil {
			upstreamTimeout = strconv.FormatInt(*componentExt.TimeoutSeconds, 10)
		}

		oauthProxyContainer, err := generateOauthProxyContainer(ctx, client, clientset, isvcName, componentMeta.Namespace, upstreamPort, upstreamTimeout)
		if err != nil {
			// return the deployment without the oauth proxy container if there was an error
			// This is required for the deployment_reconciler_tests
			return err
		}
		updatedPodSpec := deployment.Spec.Template.Spec.DeepCopy()
		//	updatedPodSpec := podSpec.DeepCopy()
		// ODH override. See : https://issues.redhat.com/browse/RHOAIENG-19904
		updatedPodSpec.AutomountServiceAccountToken = proto.Bool(true)
		updatedPodSpec.Containers = append(updatedPodSpec.Containers, *oauthProxyContainer)
		deployment.Spec.Template.Spec = *updatedPodSpec
	}
	return nil
}

func generateOauthProxyContainer(ctx context.Context, client kclient.Client, clientset kubernetes.Interface, isvc string,
	namespace string, upstreamPort string, upstreamTimeout string,
) (*corev1.Container, error) {
	// Create SAR ConfigMap for this specific InferenceService
	err := createSarCm(ctx, client, clientset, namespace, isvc)
	if err != nil {
		return nil, fmt.Errorf("failed to create SAR configmap: %w", err)
	}

	isvcConfigMap, err := clientset.CoreV1().ConfigMaps(constants.KServeNamespace).Get(ctx, constants.InferenceServiceConfigMapName, metav1.GetOptions{})
	if err != nil {
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
	oauthMemoryRequest := oauthProxyConfig.MemoryRequest
	oauthMemoryLimit := oauthProxyConfig.MemoryLimit
	oauthCpuRequest := oauthProxyConfig.CpuRequest
	oauthCpuLimit := oauthProxyConfig.CpuLimit
	oauthUpstreamTimeout := strings.TrimSpace(oauthProxyConfig.UpstreamTimeoutSeconds)
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
func createSarCm(ctx context.Context, client kclient.Client, clientset kubernetes.Interface, namespace string, inferenceServiceName string) error {
	// Get the InferenceService to obtain its UID for owner reference
	inferenceService := &v1beta1.InferenceService{}
	err := client.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      inferenceServiceName,
	}, inferenceService)
	if err != nil {
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

	// Check if configmap already exists
	existingConfigMap, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		if apierr.IsNotFound(err) {
			_, err = clientset.CoreV1().ConfigMaps(namespace).Create(ctx, sarConfigMap, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create SAR configmap: %w", err)
			}
			log.V(2).Info("Created SAR ConfigMap", "name", configMapName, "namespace", namespace)
		} else {
			return fmt.Errorf("failed to get SAR configmap: %w", err)
		}
	} else { // found
		// Since ConfigMap is immutable, if content differs we need to delete and recreate
		if existingConfigMap.Data["config-file.yaml"] != configContent {
			log.V(2).Info("SAR ConfigMap - changes detected, will be recreated", "name", configMapName, "namespace", namespace)
			err = clientset.CoreV1().ConfigMaps(namespace).Delete(ctx, configMapName, metav1.DeleteOptions{})
			if err != nil {
				return fmt.Errorf("failed to delete existing SAR configmap: %w", err)
			}
			log.V(2).Info("Deleted existing SAR ConfigMap", "name", configMapName, "namespace", namespace)

			_, err = clientset.CoreV1().ConfigMaps(namespace).Create(ctx, sarConfigMap, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to recreate SAR configmap: %w", err)
			}
			log.V(2).Info("Recreated SAR ConfigMap", "name", configMapName, "namespace", namespace)
		}
	}
	return nil
}
