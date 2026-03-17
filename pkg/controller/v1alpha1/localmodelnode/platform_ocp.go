//go:build distro

/*
Copyright 2024 The KServe Authors.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
)

const MountPath = "/var/lib/kserve"

var (
	permissionFixImage = "registry.access.redhat.com/ubi9/ubi-minimal:latest"
	validMCSLevel      = regexp.MustCompile(`^s\d+(-s\d+)?(:(c\d{1,4})(,c\d{1,4})*)?$`)
)

func enhanceDownloadJob(job *batchv1.Job, storageKey string) {
	container := &job.Spec.Template.Spec.Containers[0]
	container.VolumeMounts[0].SubPath = ""
	container.Args = []string{container.Args[0], filepath.Join(MountPath, "models", storageKey)}

	podSecurityContext := &corev1.PodSecurityContext{}
	if FSGroup != nil {
		podSecurityContext.RunAsUser = FSGroup
		podSecurityContext.RunAsGroup = FSGroup
		podSecurityContext.FSGroup = FSGroup
	}
	if mcsLevel := getProcessMCSLevel(); mcsLevel != "" {
		podSecurityContext.SELinuxOptions = &corev1.SELinuxOptions{
			Level: mcsLevel,
		}
	}
	job.Spec.Template.Spec.SecurityContext = podSecurityContext
	job.Spec.Template.Spec.ServiceAccountName = "kserve-localmodelnode-agent"
}

func ensureVolumePermissions(ctx context.Context, c *LocalModelNodeReconciler,
	localModelConfig *v1beta1.LocalModelConfig,
) (ctrl.Result, bool, error) {
	if fsHelper.isWritable() {
		return ctrl.Result{}, true, nil
	}

	c.Log.Info("Model root directory is not writable, launching permission fix job", "path", modelsRootFolder)

	isvcConfigMap, err := v1beta1.GetInferenceServiceConfigMap(ctx, c.Clientset)
	if err != nil {
		c.Log.Error(err, "unable to get configmap for permission fix image lookup")
		return ctrl.Result{}, false, err
	}
	openshiftConfig, err := v1beta1.NewOpenShiftConfig(isvcConfigMap)
	if err != nil {
		c.Log.Error(err, "Failed to get OpenShift config")
		return ctrl.Result{}, false, err
	}
	if openshiftConfig.ModelcachePermissionFixImage != "" {
		permissionFixImage = openshiftConfig.ModelcachePermissionFixImage
	}

	mcsLevel, err := c.resolveMCSLevel(ctx, localModelConfig.JobNamespace)
	if err != nil {
		c.Log.Error(err, "Invalid MCS level")
		return ctrl.Result{}, false, err
	}

	if err := c.launchPermissionFixJob(ctx, mcsLevel); err != nil {
		c.Log.Error(err, "Failed to launch permission fix job")
		return ctrl.Result{}, false, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, false, nil
}

func (c *LocalModelNodeReconciler) EnsurePermissions(ctx context.Context) error {
	if fsHelper == nil {
		fsHelper = NewFileSystemHelper(modelsRootFolder)
		if err := fsHelper.ensureModelRootFolderExists(); err != nil {
			return fmt.Errorf("failed to ensure model root folder: %w", err)
		}
	}
	if !fsHelper.isWritable() {
		c.Log.Info("Model root directory is not writable at startup, will fix in reconcile loop", "path", modelsRootFolder)
	}
	return nil
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

func (c *LocalModelNodeReconciler) launchPermissionFixJob(ctx context.Context, mcsLevel string) error {
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

	uid := os.Getuid()
	gid := os.Getgid()

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
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "kserve-localmodelnode-agent",
					NodeName:           nodeName,
					RestartPolicy:      corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						SELinuxOptions: &corev1.SELinuxOptions{
							Type:  "spc_t",
							Level: selinuxLevel,
						},
					},
					InitContainers: []corev1.Container{
						{
							Name:            "fix-ownership",
							Image:           permissionFixImage,
							Command:         []string{"chown", "-R", fmt.Sprintf("%d:%d", uid, gid), MountPath},
							SecurityContext: initSecurityContext,
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
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      PvcSourceMountName,
									MountPath: MountPath,
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
