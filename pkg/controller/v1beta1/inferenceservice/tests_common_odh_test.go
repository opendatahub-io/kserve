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

package inferenceservice

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

// getExpectedIsvcStatusODH is the ODH counterpart of getExpectedIsvcStatus: the URL scheme and
// host vary with the OpenShift exposure (https Route or cluster-local), and the address points
// at the transformer when componentHost names one.
func getExpectedIsvcStatusODH(serviceKey types.NamespacedName, protocol, host, componentHost, port string) v1beta1.InferenceServiceStatus {
	predTrans := "predictor"
	if strings.Contains(componentHost, "trans") {
		predTrans = "transformer"
	}
	if len(port) > 0 {
		port = ":" + port
	}

	return v1beta1.InferenceServiceStatus{
		Status: duckv1.Status{
			Conditions: duckv1.Conditions{
				{
					Type:   v1beta1.IngressReady,
					Status: "True",
				},
				{
					Type:   v1beta1.PredictorReady,
					Status: "True",
				},
				{
					Type:   apis.ConditionReady,
					Status: "True",
				},
				{
					Type:     v1beta1.Stopped,
					Status:   "False",
					Severity: apis.ConditionSeverityInfo,
				},
			},
		},
		URL: &apis.URL{
			Scheme: protocol,
			Host:   host,
		},
		Address: &duckv1.Addressable{
			URL: &apis.URL{
				Scheme: protocol,
				Host:   fmt.Sprintf("%s-%s.%s.svc.cluster.local%s", serviceKey.Name, predTrans, serviceKey.Namespace, port),
			},
		},
		Components: map[v1beta1.ComponentType]v1beta1.ComponentStatusSpec{
			v1beta1.PredictorComponent: {
				LatestCreatedRevision: "",
				// Status improvement from upstream is now synced
				// Component URLs always use http scheme (internal service communication)
				URL: &apis.URL{
					Scheme: "http",
					Host:   componentHost,
				},
			},
		},
		ModelStatus: v1beta1.ModelStatus{
			TransitionStatus:    "InProgress",
			ModelRevisionStates: &v1beta1.ModelRevisionStates{TargetModelState: "Pending"},
			ModelCopies:         &v1beta1.ModelCopies{},
		},
		DeploymentMode:     string(constants.Standard),
		ServingRuntimeName: "tf-serving-raw",
	}
}

// kubeRbacProxyContainer returns the kube-rbac-proxy sidecar container for an InferenceService deployment.
// If upstreamTimeoutSeconds is non-nil, --upstream-timeout=<N>s is appended to the args.
func kubeRbacProxyContainer(upstreamTimeoutSeconds *int64) corev1.Container {
	args := []string{
		`--secure-listen-address=:8443`,
		`--proxy-endpoints-port=8643`,
		`--upstream=http://localhost:8080`,
		`--auth-header-fields-enabled=true`,
		`--tls-cert-file=/etc/tls/private/tls.crt`,
		`--tls-private-key-file=/etc/tls/private/tls.key`,
		`--config-file=/etc/kube-rbac-proxy/config-file.yaml`,
		`--v=4`,
	}
	if upstreamTimeoutSeconds != nil {
		args = append(args, fmt.Sprintf("--upstream-timeout=%ds", *upstreamTimeoutSeconds))
	}
	return corev1.Container{
		Name:  constants.KubeRbacContainerName,
		Image: constants.OauthProxyImage,
		Args:  args,
		Ports: []corev1.ContainerPort{
			{ContainerPort: constants.OauthProxyPort, Name: "https", Protocol: corev1.ProtocolTCP},
			{ContainerPort: constants.OauthProxyProbePort, Name: "proxy", Protocol: corev1.ProtocolTCP},
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
				corev1.ResourceCPU:    resource.MustParse(constants.OauthProxyResourceCPULimit),
				corev1.ResourceMemory: resource.MustParse(constants.OauthProxyResourceMemoryLimit),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(constants.OauthProxyResourceCPURequest),
				corev1.ResourceMemory: resource.MustParse(constants.OauthProxyResourceMemoryRequest),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "proxy-tls", MountPath: "/etc/tls/private"},
			{Name: constants.OauthProxySARCMName, MountPath: "/etc/kube-rbac-proxy", ReadOnly: true},
		},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: "File",
		ImagePullPolicy:          "IfNotPresent",
	}
}

// proxyVolumes returns the proxy-tls and SAR ConfigMap volumes for an InferenceService deployment.
// tlsSecretName is the serving certificate Secret name; sarConfigMapName is the full SAR ConfigMap name.
func proxyVolumes(tlsSecretName, sarConfigMapName string) []corev1.Volume {
	defaultMode := int32(420)
	return []corev1.Volume{
		{
			Name: "proxy-tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  tlsSecretName,
					DefaultMode: &defaultMode,
				},
			},
		},
		{
			Name: constants.OauthProxySARCMName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: sarConfigMapName},
					DefaultMode:          &defaultMode,
				},
			},
		},
	}
}

// getExpectedDeploymentODH is the ODH counterpart of getExpectedDeployment, with the kube-rbac-proxy
// sidecar and its serving-cert volumes.
func getExpectedDeploymentODH(explainerDeploymentKey types.NamespacedName, serviceName string, serviceKey types.NamespacedName, predictorServiceKey types.NamespacedName) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      explainerDeploymentKey.Name,
			Namespace: explainerDeploymentKey.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(REPLICAS),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "isvc." + explainerDeploymentKey.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      explainerDeploymentKey.Name,
					Namespace: "default",
					Labels: map[string]string{
						"app":                                 "isvc." + explainerDeploymentKey.Name,
						constants.KServiceComponentLabel:      constants.Explainer.String(),
						constants.InferenceServicePodLabelKey: serviceName,
					},
					Annotations: getDefaultAnnotations(constants.AutoscalerClassHPA),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Image: "kserve/art-explainer:latest",
							Name:  constants.InferenceServiceContainerName,
							Args: []string{
								"--model_name",
								serviceKey.Name,
								"--http_port",
								"8080",
								"--predictor_host",
								fmt.Sprintf("%s.%s", predictorServiceKey.Name, predictorServiceKey.Namespace),
								"--adversary_type",
								"SquareAttack",
								"--nb_classes",
								"10",
							},
							Resources: defaultResource,
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.IntOrString{
											IntVal: 8080,
										},
									},
								},
								InitialDelaySeconds: 0,
								TimeoutSeconds:      1,
								PeriodSeconds:       10,
								SuccessThreshold:    1,
								FailureThreshold:    3,
							},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: "File",
							ImagePullPolicy:          "IfNotPresent",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "proxy-tls",
									MountPath: "/etc/tls/private",
								},
							},
						},
						kubeRbacProxyContainer(ptr.To(int64(30))),
					},
					Volumes: proxyVolumes(
						explainerDeploymentKey.Name+constants.ServingCertSecretSuffix,
						fmt.Sprintf("%s-%s", serviceName, constants.OauthProxySARCMName),
					),
					SchedulerName:                 "default-scheduler",
					RestartPolicy:                 "Always",
					TerminationGracePeriodSeconds: ptr.To(GRACE_PERIOD),
					DNSPolicy:                     "ClusterFirst",
					SecurityContext:               defaultSecurityContext,
					AutomountServiceAccountToken:  ptr.To(true),
				},
			},
			Strategy:                getDefaultRollingStrategy(),
			RevisionHistoryLimit:    ptr.To(REVISION_HISTORY),
			ProgressDeadlineSeconds: ptr.To(PROGRESSION_DEADLINE_SECODS),
		},
	}
}

// getDeploymentWithKServiceLabelODH is the ODH counterpart of getDeploymentWithKServiceLabel, with the
// kube-rbac-proxy sidecar and its serving-cert volumes.
func getDeploymentWithKServiceLabelODH(predictorDeploymentKey types.NamespacedName, serviceName string, isvc *v1beta1.InferenceService) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      predictorDeploymentKey.Name,
			Namespace: predictorDeploymentKey.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(REPLICAS),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "isvc." + predictorDeploymentKey.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      predictorDeploymentKey.Name,
					Namespace: "default",
					Labels: map[string]string{
						"app":                                 "isvc." + predictorDeploymentKey.Name,
						constants.KServiceComponentLabel:      constants.Predictor.String(),
						constants.InferenceServicePodLabelKey: serviceName,
					},
					Annotations: map[string]string{
						constants.StorageInitializerSourceUriInternalAnnotationKey: *isvc.Spec.Predictor.Model.StorageURI,
						constants.DeploymentMode:                                   string(constants.Standard),
						constants.AutoscalerClass:                                  string(constants.AutoscalerClassHPA),
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Image: "tensorflow/serving:" +
								*isvc.Spec.Predictor.Model.RuntimeVersion,
							Name:    constants.InferenceServiceContainerName,
							Command: []string{v1beta1.TensorflowEntrypointCommand},
							Args: []string{
								"--port=" + v1beta1.TensorflowServingGRPCPort,
								"--rest_api_port=" + v1beta1.TensorflowServingRestPort,
								"--model_base_path=" + constants.DefaultModelLocalMountPath,
								"--rest_api_timeout_in_ms=60000",
							},
							Env: []corev1.EnvVar{
								{Name: constants.InferenceServiceNameEnvVarKey, Value: serviceName},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "proxy-tls",
									MountPath: "/etc/tls/private",
								},
							},
							Resources: defaultResource,
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.IntOrString{
											IntVal: 8080,
										},
									},
								},
								InitialDelaySeconds: 0,
								TimeoutSeconds:      1,
								PeriodSeconds:       10,
								SuccessThreshold:    1,
								FailureThreshold:    3,
							},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: "File",
							ImagePullPolicy:          "IfNotPresent",
						},
						kubeRbacProxyContainer(isvc.Spec.Predictor.TimeoutSeconds),
					},
					Volumes: proxyVolumes(
						predictorDeploymentKey.Name+constants.ServingCertSecretSuffix,
						fmt.Sprintf("%s-%s", serviceName, constants.OauthProxySARCMName),
					),
					SchedulerName:                 "default-scheduler",
					RestartPolicy:                 "Always",
					TerminationGracePeriodSeconds: ptr.To(GRACE_PERIOD),
					DNSPolicy:                     "ClusterFirst",
					SecurityContext:               defaultSecurityContext,
					AutomountServiceAccountToken:  ptr.To(true),
				},
			},
			Strategy:                getDefaultRollingStrategy(),
			RevisionHistoryLimit:    ptr.To(REVISION_HISTORY),
			ProgressDeadlineSeconds: ptr.To(PROGRESSION_DEADLINE_SECODS),
		},
	}
}
