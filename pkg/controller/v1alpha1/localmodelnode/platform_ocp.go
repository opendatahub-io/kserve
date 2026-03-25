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

package localmodelnode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

const MountPath = "/var/lib/kserve"

const permFixJobFinalizerName = "serving.kserve.io/permfix-job-cleanup"

var (
	validMCSLevel = regexp.MustCompile(`^s\d+(-s\d+)?(:(c\d{1,4})(,c\d{1,4})*)?$`)

	// TODO: add rhoai image registries and check for airgapped mirrors
	allowedRegistries = []string{
		"registry.access.redhat.com/",
		"registry.redhat.io/",
		"quay.io/opendatahub/",
		"quay.io/modh/",
	}
)

func isAllowedImage(image string) bool {
	if strings.Contains(image, "..") {
		return false
	}
	for _, prefix := range allowedRegistries {
		if strings.HasPrefix(image, prefix) {
			return true
		}
	}
	return false
}

func enhanceDownloadJob(job *batchv1.Job, storageKey string) error {
	containers := job.Spec.Template.Spec.Containers
	if len(containers) == 0 || len(containers[0].VolumeMounts) == 0 || len(containers[0].Args) == 0 {
		return errors.New("download job spec is missing required containers, volume mounts, or args")
	}
	container := &job.Spec.Template.Spec.Containers[0]
	container.VolumeMounts[0].SubPath = ""
	container.Args = []string{container.Args[0], filepath.Join(MountPath, "models", storageKey)}

	podSecurityContext := &corev1.PodSecurityContext{}
	if FSGroup != nil {
		podSecurityContext.RunAsUser = FSGroup
		podSecurityContext.RunAsGroup = FSGroup
		podSecurityContext.FSGroup = FSGroup
	}
	if mcsLevel := getProcessMCSLevel(); mcsLevel != "" && validMCSLevel.MatchString(mcsLevel) {
		podSecurityContext.SELinuxOptions = &corev1.SELinuxOptions{
			Level: mcsLevel,
		}
	}
	job.Spec.Template.Spec.SecurityContext = podSecurityContext
	job.Spec.Template.Spec.ServiceAccountName = "kserve-localmodelnode-agent"
	return nil
}

func ensureModelRootFolderExistsAndIsWritable(ctx context.Context, c *LocalModelNodeReconciler,
	localModelConfig *v1beta1.LocalModelConfig,
) (*ensureModelRootFolderResult, error) {
	// Handle deletion — clean up permission fix jobs
	lmn := &v1alpha1.LocalModelNode{}
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, lmn); err == nil {
		if !lmn.DeletionTimestamp.IsZero() {
			if controllerutil.ContainsFinalizer(lmn, permFixJobFinalizerName) {
				existingJobs := &batchv1.JobList{}
				fixLabels := map[string]string{
					"fix-permissions": "true",
					"node":            nodeName,
				}
				if err := c.List(ctx, existingJobs, client.InNamespace(jobNamespace), client.MatchingLabels(fixLabels)); err == nil {
					for i := range existingJobs.Items {
						if err := c.Clientset.BatchV1().Jobs(jobNamespace).Delete(ctx, existingJobs.Items[i].Name, metav1.DeleteOptions{
							PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
						}); err != nil && !errors.IsNotFound(err) {
							c.Log.Error(err, "Failed to delete permission fix job", "job", existingJobs.Items[i].Name)
							return &ensureModelRootFolderResult{Result: ctrl.Result{RequeueAfter: 5 * time.Second}}, nil
						}
					}
				}
				controllerutil.RemoveFinalizer(lmn, permFixJobFinalizerName)
				if err := c.Update(ctx, lmn); err != nil {
					return nil, fmt.Errorf("failed to remove permission fix finalizer: %w", err)
				}
			}
			return &ensureModelRootFolderResult{}, nil
		}
	}

	// Create model root folder — tolerate permission errors
	if err := fsHelper.ensureModelRootFolderExists(); err != nil {
		if os.IsPermission(err) {
			c.Log.Info("Model root folder not writable, will launch permission fix job", "path", modelsRootFolder, "error", err)
		} else {
			return nil, fmt.Errorf("failed to ensure model root folder: %w", err)
		}
	}

	// If already writable, nothing to do
	if isModelRootWritable() {
		return &ensureModelRootFolderResult{Continue: true}, nil
	}

	c.Log.Info("Model root directory is not writable, launching permission fix job", "path", modelsRootFolder)

	// Load OpenShift config for permission fix image
	openshiftConfig, err := v1beta1.NewOpenShiftConfig(c.IsvcConfigMap)
	if err != nil {
		c.Log.Error(err, "Failed to get OpenShift config")
		return nil, err
	}

	permissionFixImage := openshiftConfig.ModelcachePermissionFixImage
	if permissionFixImage == "" {
		return nil, errors.New("modelcachePermissionFixImage not configured in inferenceservice-config")
	}
	if !isAllowedImage(permissionFixImage) {
		c.Log.Error(nil, "Rejecting permission fix image from untrusted registry", "image", permissionFixImage)
		return nil, fmt.Errorf("permission fix image %q is not from a trusted registry", permissionFixImage)
	}

	mcsLevel, err := c.resolveMCSLevel(ctx, localModelConfig.JobNamespace)
	if err != nil {
		c.Log.Error(err, "Invalid MCS level")
		return nil, err
	}

	// Re-fetch to avoid stale resourceVersion before finalizer update
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, lmn); err != nil {
		return nil, fmt.Errorf("failed to re-fetch LocalModelNode for finalizer: %w", err)
	}
	if !controllerutil.ContainsFinalizer(lmn, permFixJobFinalizerName) {
		controllerutil.AddFinalizer(lmn, permFixJobFinalizerName)
		if err := c.Update(ctx, lmn); err != nil {
			return nil, fmt.Errorf("failed to add permission fix finalizer: %w", err)
		}
	}

	if err := c.launchPermissionFixJob(ctx, mcsLevel, permissionFixImage); err != nil {
		c.Log.Error(err, "Failed to launch permission fix job")
		return nil, err
	}
	return &ensureModelRootFolderResult{Result: ctrl.Result{RequeueAfter: 10 * time.Second}}, nil
}

func getProcessMCSLevel() string {
	data, err := os.ReadFile("/proc/self/attr/current")
	if err != nil {
		return ""
	}
	parts := strings.SplitN(strings.Trim(string(data), "\x00 \n\r"), ":", 4)
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

func (c *LocalModelNodeReconciler) resolveMCSLevel(ctx context.Context, namespace string) (string, error) {
	mcsLevel := getProcessMCSLevel()
	if mcsLevel != "" {
		if !validMCSLevel.MatchString(mcsLevel) {
			return "", fmt.Errorf("invalid MCS level from process: %q", mcsLevel)
		}
		c.Log.Info("Read MCS level from agent process", "mcsLevel", mcsLevel)
		return mcsLevel, nil
	}

	ns, err := c.Clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		c.Log.Info("Could not get namespace for MCS annotation", "namespace", namespace, "error", err)
		return "", nil
	}
	if mcs, ok := ns.Annotations["openshift.io/sa.scc.mcs"]; ok {
		mcs = strings.TrimSpace(mcs)
		if !validMCSLevel.MatchString(mcs) {
			return "", fmt.Errorf("invalid MCS level from namespace annotation: %q", mcs)
		}
		c.Log.Info("Using namespace MCS level", "namespace", namespace, "mcsLevel", mcs)
		return mcs, nil
	}
	return "", nil
}

func (c *LocalModelNodeReconciler) launchPermissionFixJob(ctx context.Context, mcsLevel string, permissionFixImage string) error {
	jobName := "fix-permissions-" + nodeName

	existingJobs := &batchv1.JobList{}
	fixLabels := map[string]string{
		"fix-permissions": "true",
		"node":            nodeName,
	}
	if err := c.List(ctx, existingJobs, client.InNamespace(jobNamespace), client.MatchingLabels(fixLabels)); err != nil {
		return err
	}
	if len(existingJobs.Items) > 0 {
		job := &existingJobs.Items[0]
		if job.Status.Failed > 0 {
			c.Log.Error(fmt.Errorf("permission fix job %s failed", job.Name),
				"Ensure the service account has 'use' permission on kserve-localmodel-permissions-scc")
			_ = c.Clientset.BatchV1().Jobs(jobNamespace).Delete(ctx, job.Name, metav1.DeleteOptions{
				PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
			})
			return fmt.Errorf("permission fix job %s failed, will retry", job.Name)
		}
		c.Log.Info("Permission fix job already exists", "node", nodeName, "job", job.Name)
		return nil
	}

	pvcName := "kserve-localmodelnode-pvc"
	rootUser := int64(0)
	permFixTTL := int32(60)

	var uid, gid int64
	if FSGroup != nil {
		uid = *FSGroup
		gid = *FSGroup
	} else {
		uid = int64(os.Getuid())
		gid = int64(os.Getgid())
	}

	selinuxLevel := "s0"
	if mcsLevel != "" {
		selinuxLevel = mcsLevel
	}

	initSecurityContext := &corev1.SecurityContext{
		RunAsUser:                &rootUser,
		AllowPrivilegeEscalation: ptr.To(true),
		Capabilities: &corev1.Capabilities{
			Add: []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER"},
		},
	}

	chconCommand := []string{"chcon", "-R", "-t", "container_file_t", MountPath}
	if mcsLevel != "" {
		chconCommand = []string{"chcon", "-R", "-t", "container_file_t", "-l", mcsLevel, MountPath}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: jobName,
			Namespace:    jobNamespace,
			Labels:       fixLabels,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &permFixTTL,
			BackoffLimit:            ptr.To(int32(0)),
			ActiveDeadlineSeconds:   ptr.To(int64(120)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "kserve-localmodel-permfix",
					NodeName:           nodeName,
					RestartPolicy:      corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						SELinuxOptions: &corev1.SELinuxOptions{
							Type:  "spc_t",
							Level: selinuxLevel,
						},
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					InitContainers: []corev1.Container{
						{
							Name:            "fix-ownership",
							Image:           permissionFixImage,
							Command:         []string{"chown", "-R", fmt.Sprintf("%d:%d", uid, gid), MountPath},
							SecurityContext: initSecurityContext,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      PvcSourceMountName,
									MountPath: MountPath,
								},
							},
						},
						{
							Name:            "fix-selinux",
							Image:           permissionFixImage,
							Command:         chconCommand,
							SecurityContext: initSecurityContext,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      PvcSourceMountName,
									MountPath: MountPath,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "log-success",
							Image:   permissionFixImage,
							Command: []string{"echo", "Permissions fixed: ownership and SELinux labels applied successfully"},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								ReadOnlyRootFilesystem:   ptr.To(true),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("16Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("32Mi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: PvcSourceMountName,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
					},
				},
			},
		},
	}

	createdJob, err := c.Clientset.BatchV1().Jobs(jobNamespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create permission fix job: %w", err)
	}
	c.Log.Info("Created permission fix job", "name", createdJob.Name, "node", nodeName)
	return nil
}
