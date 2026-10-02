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
	"knative.dev/pkg/apis"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	isvcutils "github.com/kserve/kserve/pkg/controller/v1beta1/inferenceservice/utils"
)

const (
	tlsVolumeName = "proxy-tls"
)

// workloadResourceType is the kind of resource that owns the raw Deployment.
type workloadResourceType string

const (
	inferenceServiceResource workloadResourceType = "InferenceService"
	inferenceGraphResource   workloadResourceType = "InferenceGraph"
)

// AuthProxyPreservedReason marks the LatestDeploymentReady condition recorded
// while an existing auth proxy container is kept to avoid restarting its pods.
const AuthProxyPreservedReason = "AuthProxyPreserved"

// resourceTypeFor derives the owning resource kind of a component. It returns
// the zero value when the kind is unknown.
//
// Component labels include user-set metadata, so an InferenceService component
// may carry an InferenceGraph label and a graph router an InferenceService one.
// The InferenceService controller marks its reconcile context instead. Without
// that mark, the InferenceGraph label, which the graph controller always
// writes, identifies a router.
func resourceTypeFor(ctx context.Context, labels map[string]string) workloadResourceType {
	if isvcutils.IsInferenceServiceReconcile(ctx) {
		return inferenceServiceResource
	}
	if _, ok := labels[constants.InferenceGraphLabel]; ok {
		return inferenceGraphResource
	}
	return ""
}

// customizeDeployments adds the OpenShift serving certificate, auth proxy and
// transformer TLS configuration to the desired Deployments, and records the
// AuthProxyPreserved condition exposed through PlatformConditions. Audit logging
// settings resolved by the InferenceService controller apply to the predictor
// only; every other component gets AuditLoggingProfileNone, unmanaged.
func (r *DeploymentReconciler) customizeDeployments(ctx context.Context, componentMeta metav1.ObjectMeta, podSpec *corev1.PodSpec) error {
	auditLoggingProfile, manageAuditLogging := constants.AuditLoggingProfileNone, false
	if componentMeta.Labels[constants.KServiceComponentLabel] == string(v1beta1.PredictorComponent) {
		auditLoggingProfile, manageAuditLogging = isvcutils.AuditLoggingFromContext(ctx)
	}

	authProxyPreserved, err := r.customizePlatformDeployments(ctx, resourceTypeFor(ctx, componentMeta.Labels),
		componentMeta, podSpec, auditLoggingProfile, manageAuditLogging)
	if err != nil {
		return err
	}

	if authProxyPreserved {
		r.platformConditions = []apis.Condition{{
			Type:    v1beta1.LatestDeploymentReady,
			Status:  corev1.ConditionFalse,
			Reason:  AuthProxyPreservedReason,
			Message: "Preserving existing auth proxy container to avoid pod restart",
		}}
	}
	return nil
}

// customizePlatformDeployments applies the platform customizations to
// r.DeploymentList and reports whether an existing auth proxy container was
// preserved. Without a known owning resource type the auth proxy handling,
// including the lookup of the existing Deployment, is skipped.
func (r *DeploymentReconciler) customizePlatformDeployments(ctx context.Context,
	resourceType workloadResourceType,
	componentMeta metav1.ObjectMeta,
	podSpec *corev1.PodSpec,
	auditLoggingProfile constants.AuditLoggingProfile,
	manageAuditLogging bool,
) (bool, error) {
	// Add OpenShift serving cert annotation to pod template for ODH/OpenShift TLS support
	for _, deployment := range r.DeploymentList {
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = make(map[string]string)
		}
		deployment.Spec.Template.Annotations[constants.OpenshiftServingCertAnnotation] = deployment.Name + constants.ServingCertSecretSuffix
	}

	// Deployment list is for multi-node, we only need to add oauth proxy and serving secret certs to the head deployment
	headDeployment := r.DeploymentList[0]

	authProxyPreservationWarning := false
	if resourceType != "" {
		var err error
		authProxyPreservationWarning, err = r.customizeAuthProxy(ctx, resourceType, headDeployment, componentMeta, podSpec,
			auditLoggingProfile, manageAuditLogging)
		if err != nil {
			return false, err
		}
	}

	// Mount TLS infrastructure for transformer-to-predictor communication when auth is explicitly enabled
	if err := mountTransformerTLSInfrastructure(headDeployment, componentMeta); err != nil {
		return false, fmt.Errorf("failed to mount transformer TLS infrastructure: %w", err)
	}

	return authProxyPreservationWarning, nil
}

// sarVolumeNameForDeployment returns the volume name to use for the SAR ConfigMap.
// If an existing deployment already has a working legacy volume name (isvcName-kube-rbac-proxy-sar-config),
// it is preserved to avoid triggering an unnecessary deployment rollout during upgrades.
// For new deployments, it returns the fixed constant to stay within the 63-character limit.
func sarVolumeNameForDeployment(isvcName string, existingDeployment *appsv1.Deployment) string {
	if existingDeployment != nil {
		legacyName := fmt.Sprintf("%s-%s", isvcName, constants.OauthProxySARCMName)
		for _, v := range existingDeployment.Spec.Template.Spec.Volumes {
			if v.Name == legacyName {
				return legacyName
			}
		}
	}
	return constants.OauthProxySARCMName
}

// customizeAuthProxy injects, preserves or migrates the auth proxy sidecar on
// the head Deployment and mounts the serving certificate and SAR ConfigMap
// volumes. It reports whether an existing auth proxy container was preserved
// with a warning.
func (r *DeploymentReconciler) customizeAuthProxy(ctx context.Context,
	resourceType workloadResourceType,
	headDeployment *appsv1.Deployment,
	componentMeta metav1.ObjectMeta,
	podSpec *corev1.PodSpec,
	auditLoggingProfile constants.AuditLoggingProfile,
	manageAuditLogging bool,
) (bool, error) {
	// get the Inference Service Name
	var isvcname string
	if val, ok := componentMeta.Labels[constants.InferenceServicePodLabelKey]; ok {
		isvcname = val
	} else {
		isvcname = componentMeta.Name
	}

	// Check if an existing deployment is already deployed.
	existingProxyType, existingProxyImage, existingDeployment, err := getExistingAuthProxyType(ctx, r.client,
		componentMeta.Namespace, componentMeta.Name)
	if err != nil {
		return false, fmt.Errorf("failed to fetch deployment %s/%s: %w", componentMeta.Namespace, componentMeta.Name, err)
	}
	existingDeploymentFound := existingDeployment != nil

	sarVolumeName := sarVolumeNameForDeployment(isvcname, existingDeployment)

	// shouldAddAuthProxy controls whether the OAuth proxy sidecar is injected or preserved.
	// For InferenceService: always inject for new deployments (to avoid pod-template rollouts
	// when auth is later toggled), preserve for existing deployments that already carry the
	// proxy, and also inject when auth is explicitly enabled via annotation.
	// Transformer deployments must NOT receive the auth proxy — only the predictor needs
	// the sidecar; the transformer communicates with the predictor over TLS instead.
	isTransformer := componentMeta.Labels[constants.KServiceComponentLabel] == string(v1beta1.TransformerComponent)
	shouldAddAuthProxy := false
	if resourceType == inferenceServiceResource && !isTransformer {
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

	authProxyReused := false
	authProxyPreservationWarning := false
	refreshPreservedSARConfig := false
	if shouldAddAuthProxy {
		auditConfigChanged := platformAuthProxyNeedsUpdate(auditLoggingProfile, manageAuditLogging, existingDeployment, componentMeta, isvcname)
		wantsMigration := false
		if val, ok := componentMeta.Annotations[constants.ODHAuthProxyTypeAnnotation]; ok {
			wantsMigration = val == constants.KubeRbacProxyType
		}

		oauthConfig, cfgErr := getOauthProxyConfig(ctx, r.clientset)
		if cfgErr != nil {
			oauthConfig = nil
		}

		if existingProxyType != "" {
			switch existingProxyType {
			case constants.OauthProxyContainerName:
				if wantsMigration {
					err := addOauthContainerToDeployment(ctx, r.client, r.clientset, oauthConfig, headDeployment, componentMeta, r.componentExt, podSpec, isvcname, sarVolumeName, auditLoggingProfile, manageAuditLogging)
					if err != nil {
						return false, err
					}
				} else {
					log.Info("Preserving existing auth proxy container", "isvc", isvcname, "type", existingProxyType)
					authProxyReused = true
					authProxyPreservationWarning = true
					copyAuthProxyFromExisting(existingDeployment, headDeployment, existingProxyType)
				}
			case constants.KubeRbacContainerName:
				configuredKubeRbacImage := ""
				if oauthConfig != nil {
					configuredKubeRbacImage = oauthConfig.Image
				}
				configuredImageMatches := configuredKubeRbacImage != "" && existingProxyImage == configuredKubeRbacImage
				if auditConfigChanged {
					err := addOauthContainerToDeployment(ctx, r.client, r.clientset, oauthConfig, headDeployment, componentMeta, r.componentExt, podSpec, isvcname, sarVolumeName, auditLoggingProfile, manageAuditLogging)
					if err != nil {
						return false, err
					}
				} else {
					log.Info("Preserving existing auth proxy container",
						"isvc", isvcname, "type", existingProxyType,
						"existingImage", existingProxyImage, "configImage", configuredKubeRbacImage)
					authProxyReused = true
					authProxyPreservationWarning = !configuredImageMatches
					refreshPreservedSARConfig = configuredImageMatches
					copyAuthProxyFromExisting(existingDeployment, headDeployment, existingProxyType)
				}
			}
		} else {
			err := addOauthContainerToDeployment(ctx, r.client, r.clientset, oauthConfig, headDeployment, componentMeta, r.componentExt, podSpec, isvcname, sarVolumeName, auditLoggingProfile, manageAuditLogging)
			if err != nil {
				return false, err
			}
		}
	}
	if refreshPreservedSARConfig {
		if err := createSarCm(ctx, r.client, r.clientset, componentMeta.Namespace, isvcname); err != nil {
			return false, fmt.Errorf("failed to refresh preserved SAR configmap: %w", err)
		}
	}
	if (shouldAddAuthProxy && !authProxyReused) || resourceType == inferenceGraphResource {
		mountServingSecretCMVolumeToDeployment(headDeployment, componentMeta, resourceType, isvcname, sarVolumeName)
	}

	return authProxyPreservationWarning, nil
}

func mountServingSecretCMVolumeToDeployment(deployment *appsv1.Deployment, componentMeta metav1.ObjectMeta, resourceType workloadResourceType, isvcName string, sarVolumeName string) {
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
		Name: sarVolumeName,
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
	if resourceType == inferenceGraphResource {
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
	oauthConfig *v1beta1.OauthConfig,
	deployment *appsv1.Deployment,
	componentMeta metav1.ObjectMeta,
	componentExt *v1beta1.ComponentExtensionSpec,
	podSpec *corev1.PodSpec, isvcName string, sarVolumeName string,
	auditLoggingProfile constants.AuditLoggingProfile,
	manageAuditLogging bool,
) error {
	var upstreamPort, upstreamTimeout string

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

	oauthProxyContainer, err := generateOauthProxyContainer(ctx, client, clientset, oauthConfig, isvcName, componentMeta.Namespace, upstreamPort, upstreamTimeout, sarVolumeName)
	if err != nil {
		return err
	}
	oauthProxyContainer.Args = customizeAuthProxyArgs(auditLoggingProfile, manageAuditLogging, componentMeta, oauthProxyContainer.Args, isvcName)
	updatedPodSpec := deployment.Spec.Template.Spec.DeepCopy()
	// ODH override. See: https://issues.redhat.com/browse/RHOAIENG-19904
	updatedPodSpec.AutomountServiceAccountToken = proto.Bool(true)
	updatedPodSpec.Containers = append(updatedPodSpec.Containers, *oauthProxyContainer)
	deployment.Spec.Template.Spec = *updatedPodSpec
	return nil
}

func GetKServeContainerPort(podSpec *corev1.PodSpec) string {
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

func generateOauthProxyContainer(ctx context.Context, client kclient.Client, clientset kubernetes.Interface,
	oauthConfig *v1beta1.OauthConfig, isvc string, namespace string, upstreamPort string, upstreamTimeout string,
	sarVolumeName string,
) (*corev1.Container, error) {
	// Create SAR ConfigMap for this specific InferenceService
	err := createSarCm(ctx, client, clientset, namespace, isvc)
	if err != nil {
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
				Name:      sarVolumeName,
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

// copyAuthProxyFromExisting copies the auth proxy container and only its related
// volumes/mounts from an existing deployment into the desired deployment, preserving
// any user-defined volumes and mounts already present on the desired spec.
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

// getExistingAuthProxyType checks if the deployment already has an auth proxy container.
// Returns the container name ("oauth-proxy" or "kube-rbac-proxy"), its image, and the
// existing deployment for use in preservation logic.
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

// getOauthProxyConfig fetches and parses the oauth proxy configuration from the inferenceservice configmap.
func getOauthProxyConfig(ctx context.Context, clientset kubernetes.Interface) (*v1beta1.OauthConfig, error) {
	isvcConfigMap, err := clientset.CoreV1().ConfigMaps(constants.KServeNamespace).Get(ctx, constants.InferenceServiceConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	oauthProxyJSON := strings.TrimSpace(isvcConfigMap.Data["oauthProxy"])
	oauthProxyConfig := &v1beta1.OauthConfig{}
	if err := json.Unmarshal([]byte(oauthProxyJSON), oauthProxyConfig); err != nil {
		return nil, err
	}
	return oauthProxyConfig, nil
}

// mountTransformerTLSInfrastructure injects TLS volumes and env vars into the
// transformer deployment's kserve-container. It adds:
//  1. The OpenShift service-ca bundle (CA trust for outbound TLS to the predictor)
//  2. The transformer's own serving certificate (for native HTTPS on port 8443)
//  3. Env vars for predictor endpoint discovery and serving-cert paths
func mountTransformerTLSInfrastructure(deployment *appsv1.Deployment, componentMeta metav1.ObjectMeta) error {
	// Only inject TLS infrastructure when auth is enabled and this is the transformer component.
	authEnabled, ok := componentMeta.Annotations[constants.ODHKserveRawAuth]
	if !ok || !strings.EqualFold(authEnabled, "true") {
		return nil
	}
	componentLabel, ok := componentMeta.Labels[constants.KServiceComponentLabel]
	if !ok || componentLabel != string(v1beta1.TransformerComponent) {
		return nil
	}

	// Validate isvcName before any mutation to avoid orphaned volumes
	isvcName := componentMeta.Labels[constants.InferenceServicePodLabelKey]
	if isvcName == "" {
		return fmt.Errorf("InferenceServicePodLabelKey label missing on transformer deployment %q", componentMeta.Name)
	}

	podSpec := &deployment.Spec.Template.Spec

	// Add openshift-service-ca.crt ConfigMap volume (CA trust for outbound TLS to predictor)
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: constants.ServiceCaBundleVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: constants.OpenShiftServiceCaConfigMapName,
				},
			},
		},
	})

	// Add transformer serving-cert volume (for the transformer's own HTTPS endpoint)
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: constants.TransformerTLSVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: componentMeta.Name + constants.ServingCertSecretSuffix,
			},
		},
	})

	predictorHost := fmt.Sprintf("%s.%s.svc",
		constants.PredictorServiceName(isvcName), componentMeta.Namespace)

	// Add volume mounts + env vars to kserve-container
	containerFound := false
	for i, container := range podSpec.Containers {
		if container.Name == constants.InferenceServiceContainerName {
			containerFound = true
			podSpec.Containers[i].VolumeMounts = append(
				podSpec.Containers[i].VolumeMounts,
				corev1.VolumeMount{
					Name:      constants.ServiceCaBundleVolumeName,
					MountPath: constants.ServiceCaBundleMountPath,
					ReadOnly:  true,
				},
				corev1.VolumeMount{
					Name:      constants.TransformerTLSVolumeName,
					MountPath: constants.TransformerTLSMountPath,
					ReadOnly:  true,
				},
			)
			podSpec.Containers[i].Env = append(podSpec.Containers[i].Env,
				corev1.EnvVar{
					Name:  "SSL_CERT_DIR",
					Value: constants.ServiceCaBundleMountPath,
				},
				corev1.EnvVar{
					Name:  "REQUESTS_CA_BUNDLE",
					Value: constants.ServiceCaBundleMountPath + "/" + constants.ServiceCaBundleCertFile,
				},
				corev1.EnvVar{
					Name:  constants.PredictorHostEnvVar,
					Value: predictorHost,
				},
				corev1.EnvVar{
					Name:  constants.PredictorPortEnvVar,
					Value: strconv.Itoa(constants.OauthProxyPort),
				},
				corev1.EnvVar{
					Name:  constants.PredictorProtocolEnvVar,
					Value: "https",
				},
				corev1.EnvVar{
					Name:  constants.TransformerTLSCertEnvVar,
					Value: constants.TransformerTLSMountPath + "/tls.crt",
				},
				corev1.EnvVar{
					Name:  constants.TransformerTLSKeyEnvVar,
					Value: constants.TransformerTLSMountPath + "/tls.key",
				},
			)
			// Inject --predictor_use_ssl=true so the Python SDK uses https:// for predictor_base_url
			podSpec.Containers[i].Args = append(podSpec.Containers[i].Args,
				constants.ArgumentPredictorUseSSL, "true",
			)

			// Determine the serving port. GetContainer() auto-injects
			// "--http_port 8080" before this hook runs, so the framework
			// default is indistinguishable from "absent". Override it to
			// the HTTPS port; if the user explicitly set a non-default
			// port, respect their choice.
			servingPort := constants.TransformerHTTPSPort
			if userPort, ok := getArgValue(podSpec.Containers[i].Args, constants.ArgumentHttpPort); ok {
				if userPort != constants.InferenceServiceDefaultHttpPort {
					if parsed, err := strconv.ParseInt(userPort, 10, 32); err == nil {
						servingPort = int32(parsed)
					}
				}
			}
			podSpec.Containers[i].Args = setArgValue(podSpec.Containers[i].Args,
				constants.ArgumentHttpPort, strconv.Itoa(int(servingPort)))

			podSpec.Containers[i].Ports = append(podSpec.Containers[i].Ports, corev1.ContainerPort{
				ContainerPort: servingPort,
				Protocol:      corev1.ProtocolTCP,
			})

			// Patch existing probes to target the serving port.
			// setDefaultPodSpec() already created a readiness probe on the --http_port
			// value (8080 by default) before this hook runs; update it to match the
			// actual listening port after the override above.
			portVal := intstr.IntOrString{IntVal: servingPort}
			if podSpec.Containers[i].ReadinessProbe != nil && podSpec.Containers[i].ReadinessProbe.TCPSocket != nil {
				podSpec.Containers[i].ReadinessProbe.TCPSocket.Port = portVal
			}
			if podSpec.Containers[i].LivenessProbe != nil && podSpec.Containers[i].LivenessProbe.TCPSocket != nil {
				podSpec.Containers[i].LivenessProbe.TCPSocket.Port = portVal
			}
			break
		}
	}
	if !containerFound {
		return fmt.Errorf("container %q not found in transformer deployment %q", constants.InferenceServiceContainerName, componentMeta.Name)
	}
	return nil
}

// customizeAuthProxyArgs replaces only controller-managed audit arguments with
// the explicitly resolved effective profile.
func customizeAuthProxyArgs(profile constants.AuditLoggingProfile, manage bool, componentMeta metav1.ObjectMeta, generated []string, isvcName string) []string {
	if !manage {
		return generated
	}
	args := removeManagedAuditArgs(generated)
	return append(args, desiredAuditArgs(profile, componentMeta, isvcName)...)
}

// platformAuthProxyNeedsUpdate reports whether a kube-rbac-proxy's managed
// audit arguments differ from the resolved effective profile.
func platformAuthProxyNeedsUpdate(profile constants.AuditLoggingProfile, manage bool, existing *appsv1.Deployment, componentMeta metav1.ObjectMeta, isvcName string) bool {
	if existing == nil || !manage {
		return false
	}
	desired := desiredAuditArgs(profile, componentMeta, isvcName)
	for _, container := range existing.Spec.Template.Spec.Containers {
		if container.Name == constants.KubeRbacContainerName {
			return !sameArgs(managedAuditArgs(container.Args), desired)
		}
	}
	return false
}

// desiredAuditArgs builds controller-managed arguments for the effective profile.
func desiredAuditArgs(profile constants.AuditLoggingProfile, componentMeta metav1.ObjectMeta, isvcName string) []string {
	if profile != constants.AuditLoggingProfileMetadata {
		return nil
	}
	name := componentMeta.Labels[constants.InferenceServicePodLabelKey]
	if name == "" {
		name = isvcName
	}
	return []string{
		"--audit-log-profile=metadata",
		"--audit-resource-name=" + name,
		"--audit-resource-namespace=" + componentMeta.Namespace,
		"--audit-resource-type=InferenceService",
		"--audit-ai-provider=KServe",
	}
}

// removeManagedAuditArgs removes every kube-rbac-proxy argument in the
// controller-owned audit namespace while leaving unrelated arguments intact.
func removeManagedAuditArgs(args []string) []string {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if !isManagedAuditArg(arg) {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}

// managedAuditArgs returns the controller-owned audit arguments in their
// original order for exact desired-state comparison.
func managedAuditArgs(args []string) []string {
	result := make([]string, 0)
	for _, arg := range args {
		if isManagedAuditArg(arg) {
			result = append(result, arg)
		}
	}
	return result
}

// sameArgs reports whether two ordered argument slices are identical.
func sameArgs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// isManagedAuditArg reports whether an argument belongs to the controller-owned
// kube-rbac-proxy audit namespace.
func isManagedAuditArg(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	return strings.HasPrefix(name, "--audit-")
}
