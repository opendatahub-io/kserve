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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
	controllerutils "github.com/kserve/kserve/pkg/controller/v1alpha1/utils"
	"github.com/kserve/kserve/pkg/credentials/s3"
)

const MountPath = "/var/lib/kserve"

const odhCustomCABundleConfigMapName = "odh-kserve-custom-ca-bundle"

var implicitODHS3CABundlePath = filepath.Join(constants.DefaultCaBundleVolumeMountPath, constants.DefaultCaBundleFileName)

// sharedMCSLevel is the category-less SELinux MCS level applied to the cached model tree.
// The cache is a shared, read-only, cross-namespace hostPath artifact: a directory can carry
// exactly one SELinux label, and hostPath / local PVs are never relabeled per-consumer by the
// kubelet. An empty category set is a subset of every consumer namespace's categories, so a
// category-less level is the only label readable by all consumers simultaneously.
const sharedMCSLevel = "s0"

// Categories may be comma-separated (typical on OCP) or dot-separated (e.g. Fedora: s0-s0:c0.c1023).
var validMCSLevel = regexp.MustCompile(`^s\d+(-s\d+)?(:c\d{1,4}([,.]c\d{1,4})*)?$`)

// maxRelabelAttempts bounds how many consecutive reconciles may launch a permission-fix job for a
// model folder that never reaches the shared MCS level. Without it, a folder whose label can never
// be cleared — a read-only mount, a filesystem that rejects the relabel, or a persistent xattr
// read error — would recreate a privileged relabel job every reconcile once the previous job's TTL
// expires, an unbounded privileged-pod churn (CWE-400). The count is per model folder and resets
// as soon as that folder reads at the shared level, so legitimate sequential downloads (each
// relabeled successfully) never trip the cap.
const maxRelabelAttempts = 20

var (
	relabelAttemptsMu sync.Mutex
	relabelAttempts   = map[string]int{}
)

func recordRelabelAttempt(path string) {
	relabelAttemptsMu.Lock()
	defer relabelAttemptsMu.Unlock()
	relabelAttempts[path]++
}

func relabelAttemptCount(path string) int {
	relabelAttemptsMu.Lock()
	defer relabelAttemptsMu.Unlock()
	return relabelAttempts[path]
}

func resetRelabelAttempts(path string) {
	relabelAttemptsMu.Lock()
	defer relabelAttemptsMu.Unlock()
	delete(relabelAttempts, path)
}

func (c *LocalModelNodeReconciler) enhanceDownloadJob(ctx context.Context, job *batchv1.Job, storageKey string) error {
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
	mcsLevel, err := c.resolveMCSLevel(ctx, jobNamespace)
	if err != nil {
		return fmt.Errorf("failed to resolve MCS level: %w", err)
	}
	podSecurityContext.SELinuxOptions = &corev1.SELinuxOptions{
		Level: mcsLevel,
	}
	job.Spec.Template.Spec.SecurityContext = podSecurityContext
	job.Spec.Template.Spec.ServiceAccountName = "kserve-localmodelnode-agent"
	if err := c.mountODHCustomCABundle(ctx, job); err != nil {
		return fmt.Errorf("failed to configure ODH custom CA bundle: %w", err)
	}
	return nil
}

// mountODHCustomCABundle adds the namespace-local bundle produced by the model controller.
// Missing or empty bundles leave the download job on its default trust store.
func (c *LocalModelNodeReconciler) mountODHCustomCABundle(ctx context.Context, job *batchv1.Job) error {
	if job == nil || len(job.Spec.Template.Spec.Containers) == 0 {
		return nil
	}

	container := &job.Spec.Template.Spec.Containers[0]
	envState := inspectCABundleEnv(container)
	// A named, different CA remains entirely caller-owned. OCI and S3 setup can
	// also reference this managed ConfigMap explicitly; those Jobs still need to
	// wait until the namespace copy is current. A nonempty ValueFrom is likewise
	// caller-owned because its value cannot be compared safely here.
	if !envState.managedConfigMap && (envState.explicitConfigMap || envState.mountPathFrom || envState.explicitCA) {
		return nil
	}

	reader := c.APIReader
	if reader == nil {
		reader = c.Client
	}
	if reader == nil {
		return errors.New("no Kubernetes reader configured for CA bundle lookup")
	}
	// The applications namespace is the source of truth. Wait for the LocalModel
	// controller to publish the same bundle beside the Job before mounting it.
	// A missing source and target intentionally use the system trust store.
	sourceNamespace := controllerutils.InferenceServiceConfigNamespace()
	var sourceBundle string
	if sourceNamespace != job.Namespace {
		source := &corev1.ConfigMap{}
		err := reader.Get(ctx, types.NamespacedName{
			Name: odhCustomCABundleConfigMapName, Namespace: sourceNamespace,
		}, source)
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("reading applications namespace CA bundle: %w", err)
		}
		if err == nil {
			sourceBundle = source.Data[constants.DefaultCaBundleFileName]
		}
	}

	caBundleConfigMap := &corev1.ConfigMap{}
	err := reader.Get(ctx, types.NamespacedName{
		Name:      odhCustomCABundleConfigMapName,
		Namespace: job.Namespace,
	}, caBundleConfigMap)
	if apierrors.IsNotFound(err) {
		if strings.TrimSpace(sourceBundle) != "" {
			return fmt.Errorf("waiting for CA bundle ConfigMap %s in job namespace %s", odhCustomCABundleConfigMapName, job.Namespace)
		}
		if envState.managedConfigMap {
			// The ODH config uses the managed bundle as a default for S3 jobs. An
			// absent source means that no custom CA is configured, so remove the
			// default wiring added by mountCaBundleVolume and use system trust.
			removeImplicitODHBundle(job, envState)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if sourceNamespace != job.Namespace {
		targetBundle := strings.TrimSpace(caBundleConfigMap.Data[constants.DefaultCaBundleFileName])
		if strings.TrimSpace(sourceBundle) == "" && targetBundle == "" {
			if envState.managedConfigMap {
				removeImplicitODHBundle(job, envState)
			}
			return nil
		}
		if strings.TrimSpace(sourceBundle) == "" || caBundleConfigMap.Data[constants.DefaultCaBundleFileName] != sourceBundle {
			return fmt.Errorf("waiting for CA bundle ConfigMap %s in job namespace %s to match applications namespace %s",
				odhCustomCABundleConfigMapName, job.Namespace, sourceNamespace)
		}
	}
	if strings.TrimSpace(caBundleConfigMap.Data[constants.DefaultCaBundleFileName]) == "" {
		if envState.managedConfigMap {
			removeImplicitODHBundle(job, envState)
		}
		return nil
	}

	mountPath := caBundleMountPath(envState)
	volumeIndex := -1
	for index, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name != CaBundleVolumeName {
			continue
		}
		if volume.ConfigMap == nil || volume.ConfigMap.Name != odhCustomCABundleConfigMapName {
			// A caller-owned volume already uses the conventional name. Do not
			// replace or reinterpret it as the ODH bundle.
			return nil
		}
		volumeIndex = index
		break
	}
	if volumeIndex >= 0 {
		for _, volumeMount := range container.VolumeMounts {
			if volumeMount.Name != CaBundleVolumeName {
				continue
			}
			if !sameCABundlePath(volumeMount.MountPath, envState) {
				// Leave a caller-owned explicit path untouched if it does not
				// describe the generated mount. The caller may be using a file
				// name that is not the ConfigMap's default key.
				return nil
			}
			mountPath = volumeMount.MountPath
			setCABundleEnvIfEmpty(container, constants.CaBundleVolumeMountPathEnvVarKey, mountPath)
			return nil
		}
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name: CaBundleVolumeName, MountPath: mountPath, ReadOnly: true,
		})
		setCABundleEnvIfEmpty(container, constants.CaBundleVolumeMountPathEnvVarKey, mountPath)
		return nil
	}
	for _, volumeMount := range container.VolumeMounts {
		if volumeMount.Name == CaBundleVolumeName || volumeMount.MountPath == mountPath {
			return nil
		}
	}
	optional := true
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: CaBundleVolumeName,
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: odhCustomCABundleConfigMapName},
			Optional:             &optional,
		}},
	})
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name: CaBundleVolumeName, MountPath: mountPath, ReadOnly: true,
	})
	setCABundleEnvIfEmpty(container, constants.CaBundleConfigMapNameEnvVarKey, odhCustomCABundleConfigMapName)
	setCABundleEnvIfEmpty(container, constants.CaBundleVolumeMountPathEnvVarKey, mountPath)

	c.Log.Info("Mounted ODH custom CA bundle ConfigMap", "configMap", odhCustomCABundleConfigMapName,
		"namespace", job.Namespace, "mountPath", mountPath)
	return nil
}

type caBundleEnvState struct {
	managedConfigMap  bool
	explicitConfigMap bool
	explicitCA        bool
	awsCABundlePath   string
	mountPath         string
	mountPathFrom     bool
}

func inspectCABundleEnv(container *corev1.Container) caBundleEnvState {
	state := caBundleEnvState{}
	for _, env := range container.Env {
		if env.Name == constants.CaBundleConfigMapNameEnvVarKey || env.Name == s3.AWSCABundleConfigMap {
			if env.ValueFrom != nil {
				state.explicitConfigMap = true
				continue
			}
			switch env.Value {
			case odhCustomCABundleConfigMapName:
				state.managedConfigMap = true
			case "":
			default:
				state.explicitConfigMap = true
			}
		}
	}
	for _, env := range container.Env {
		switch env.Name {
		case s3.AWSCABundle:
			if env.ValueFrom != nil {
				state.explicitCA = true
			} else if env.Value != "" {
				state.awsCABundlePath = env.Value
				if !state.managedConfigMap {
					state.explicitCA = true
				}
			}
		case "REQUESTS_CA_BUNDLE", "SSL_CERT_FILE":
			if env.ValueFrom != nil || env.Value != "" {
				state.explicitCA = true
			}
		case constants.CaBundleVolumeMountPathEnvVarKey:
			if env.ValueFrom != nil {
				state.mountPathFrom = true
			} else if env.Value != "" {
				state.mountPath = env.Value
			}
		}
	}
	return state
}

func caBundleMountPath(state caBundleEnvState) string {
	if state.mountPathFrom {
		return constants.DefaultCaBundleVolumeMountPath
	}
	if state.mountPath != "" && state.mountPath != constants.DefaultCaBundleVolumeMountPath {
		return state.mountPath
	}
	if state.awsCABundlePath != "" && filepath.Clean(state.awsCABundlePath) != filepath.Clean(implicitODHS3CABundlePath) {
		if path := filepath.Dir(state.awsCABundlePath); path != "." && path != string(filepath.Separator) {
			return path
		}
	}
	if storageInitializerConfig != nil && storageInitializerConfig.CaBundleVolumeMountPath != "" {
		return storageInitializerConfig.CaBundleVolumeMountPath
	}
	return constants.DefaultCaBundleVolumeMountPath
}

func sameCABundlePath(path string, state caBundleEnvState) bool {
	if path == "" {
		return false
	}
	if state.mountPath != "" && state.mountPath != constants.DefaultCaBundleVolumeMountPath && path != state.mountPath {
		return false
	}
	if state.awsCABundlePath != "" && filepath.Clean(state.awsCABundlePath) != filepath.Clean(implicitODHS3CABundlePath) &&
		filepath.Dir(state.awsCABundlePath) != path {
		return false
	}
	return true
}

func removeImplicitODHBundle(job *batchv1.Job, state caBundleEnvState) {
	if job == nil || len(job.Spec.Template.Spec.Containers) == 0 {
		return
	}
	container := &job.Spec.Template.Spec.Containers[0]
	implicitPath := state.awsCABundlePath == "" || filepath.Clean(state.awsCABundlePath) == filepath.Clean(implicitODHS3CABundlePath)
	// A non-default AWS_CA_BUNDLE is caller-owned, so keep that env var while
	// removing the separately generated ODH ConfigMap wiring. It may be backed
	// by a caller-supplied volume. CA_BUNDLE_VOLUME_MOUNT_POINT only selects
	// where the managed bundle is mounted, so its generated default is removed
	// with the ODH volume when no bundle exists.
	filteredEnv := container.Env[:0]
	for _, env := range container.Env {
		remove := false
		switch env.Name {
		case s3.AWSCABundleConfigMap, constants.CaBundleConfigMapNameEnvVarKey:
			remove = env.Value == odhCustomCABundleConfigMapName
		case s3.AWSCABundle:
			remove = implicitPath && env.Value == implicitODHS3CABundlePath
		case constants.CaBundleVolumeMountPathEnvVarKey:
			remove = !state.mountPathFrom && (env.Value == "" || env.Value == constants.DefaultCaBundleVolumeMountPath)
		}
		if !remove {
			filteredEnv = append(filteredEnv, env)
		}
	}
	container.Env = filteredEnv
	filteredVolumes := job.Spec.Template.Spec.Volumes[:0]
	for _, volume := range job.Spec.Template.Spec.Volumes {
		remove := volume.Name == CaBundleVolumeName && volume.ConfigMap != nil && volume.ConfigMap.Name == odhCustomCABundleConfigMapName
		if !remove {
			filteredVolumes = append(filteredVolumes, volume)
		}
	}
	job.Spec.Template.Spec.Volumes = filteredVolumes
	filteredMounts := container.VolumeMounts[:0]
	for _, mount := range container.VolumeMounts {
		if mount.Name != CaBundleVolumeName {
			filteredMounts = append(filteredMounts, mount)
		}
	}
	container.VolumeMounts = filteredMounts
}

func setCABundleEnvIfEmpty(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name != name {
			continue
		}
		if container.Env[i].Value == "" && container.Env[i].ValueFrom == nil {
			container.Env[i].Value = value
		}
		return
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}

func (c *LocalModelNodeReconciler) ensureModelRootFolderExistsAndIsWritable(ctx context.Context,
) (*ensureModelRootFolderResult, error) {
	needsPermFix := false

	// Create model root folder — tolerate permission errors
	if err := fsHelper.ensureModelRootFolderExists(); err != nil {
		if os.IsPermission(err) {
			c.Log.Info("Model root folder not writable, need permission fix", "path", modelsRootFolder, "error", err)
			needsPermFix = true
		} else {
			return nil, fmt.Errorf("failed to ensure model root folder: %w", err)
		}
	}

	// Check existing subdirectories for permission issues — a download job or prior
	// agent run may have created subdirectories with different ownership (e.g.
	// root:root), leaving them inaccessible to the current agent UID.
	if !needsPermFix && isModelRootWritable() {
		entries, readErr := os.ReadDir(modelsRootFolder)
		if readErr != nil && os.IsPermission(readErr) {
			needsPermFix = true
		} else if readErr == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				subdir := filepath.Join(modelsRootFolder, entry.Name())
				if _, err := os.ReadDir(subdir); err != nil {
					if os.IsPermission(err) {
						c.Log.Info("Subdirectory has permission issues", "path", subdir, "error", err)
						needsPermFix = true
						break
					}
					// Non-permission error (e.g. the entry was removed between listing the
					// parent and this read): log and skip rather than probing the SELinux
					// label of a path that may no longer exist.
					c.Log.Info("Skipping unreadable subdirectory", "path", subdir, "error", err)
					continue
				}
				// A folder carrying MCS categories (i.e. not the shared level) is unreadable from
				// a consuming namespace whose category set does not dominate it — the download job
				// writes files at the job namespace's MCS, so the agent (same MCS) reads fine and
				// marks NodeDownloaded, but a serving pod in another namespace is denied. Relabel
				// to the shared level. This also self-heals caches downloaded before this fix.
				categorized, mcsErr := folderHasNonSharedMCS(subdir)
				if mcsErr != nil || categorized {
					// Bound retries. A folder that can never reach the shared level (read-only
					// mount, a filesystem that rejects the relabel, or a persistent xattr read
					// error) must not recreate a privileged relabel job every reconcile once the
					// prior job's TTL expires. launchPermissionFixJob's dedupe only holds while a
					// job object exists — it does not survive the TTL GC — so it is not a loop
					// guard on its own. After the cap, give up on this folder and surface an error.
					if relabelAttemptCount(subdir) >= maxRelabelAttempts {
						c.Log.Error(mcsErr, "Giving up on SELinux relabel after repeated attempts; manual intervention required",
							"path", subdir, "attempts", maxRelabelAttempts, "categorized", categorized)
						continue
					}
					recordRelabelAttempt(subdir)
					if mcsErr != nil {
						c.Log.Info("Could not read SELinux label, relabeling conservatively", "path", subdir, "error", mcsErr)
					} else {
						c.Log.Info("Subdirectory carries non-shared MCS categories, needs relabel", "path", subdir)
					}
					needsPermFix = true
					break
				}
				// Folder is at the shared level — clear any prior failed-attempt count.
				resetRelabelAttempts(subdir)
			}
		}
		if !needsPermFix {
			return &ensureModelRootFolderResult{Continue: true}, nil
		}
	}

	c.Log.Info("Launching permission fix job", "path", modelsRootFolder)

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
	// Fetch the LocalModelNode to set as owner of the permission fix job
	lmn := &v1alpha1.LocalModelNode{}
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, lmn); err != nil {
		return nil, fmt.Errorf("failed to get LocalModelNode for owner reference: %w", err)
	}

	if err := c.launchPermissionFixJob(ctx, permissionFixImage, lmn); err != nil {
		c.Log.Error(err, "Failed to launch permission fix job")
		return nil, err
	}
	return &ensureModelRootFolderResult{Result: ctrl.Result{RequeueAfter: 10 * time.Second}}, nil
}

// parseMCSLevel extracts the SELinux level (sensitivity plus optional categories) from a full
// SELinux context string, e.g. "system_u:object_r:container_file_t:s0:c2,c28" -> "s0:c2,c28".
// It MUST use SplitN with a limit of 4: the level itself contains a colon, so a plain
// Split(":")[3] would return only "s0", drop the categories, and silently defeat MCS handling.
// Returns "" when the context has fewer than 4 colon-separated segments.
func parseMCSLevel(selinuxContext string) string {
	parts := strings.SplitN(strings.Trim(selinuxContext, "\x00 \n\r"), ":", 4)
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

func getProcessMCSLevel() string {
	data, err := os.ReadFile("/proc/self/attr/current")
	if err != nil {
		return ""
	}
	return parseMCSLevel(string(data))
}

func (c *LocalModelNodeReconciler) resolveMCSLevel(ctx context.Context, namespace string) (string, error) {
	ns, err := c.Clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		c.Log.Info("Could not get namespace for MCS annotation", "namespace", namespace, "error", err)
		return c.resolveMCSLevelFromProcessOrDefault()
	}

	// When the job namespace carries openshift.io/sa.scc.mcs, it must win over the
	// agent process MCS so chcon targets the workload namespace (OCP). If the
	// annotation is present but invalid, reject — do not fall back to process MCS
	// (security: blocks injection attempts that would be ignored on OCP).
	if mcs, ok := ns.Annotations["openshift.io/sa.scc.mcs"]; ok {
		mcs = strings.TrimSpace(mcs)
		if mcs != "" {
			if !validMCSLevel.MatchString(mcs) {
				return "", fmt.Errorf("invalid MCS level from namespace annotation: %q", mcs)
			}
			c.Log.Info("Using namespace MCS level", "namespace", namespace, "mcsLevel", mcs)
			return mcs, nil
		}
	}

	return c.resolveMCSLevelFromProcessOrDefault()
}

func (c *LocalModelNodeReconciler) resolveMCSLevelFromProcessOrDefault() (string, error) {
	mcsLevel := getProcessMCSLevel()
	if mcsLevel == "" {
		return "s0", nil
	}
	if !validMCSLevel.MatchString(mcsLevel) {
		return "", fmt.Errorf("invalid MCS level from process: %q", mcsLevel)
	}
	c.Log.Info("Read MCS level from agent process", "mcsLevel", mcsLevel)
	return mcsLevel, nil
}

func (c *LocalModelNodeReconciler) launchPermissionFixJob(ctx context.Context, permissionFixImage string, owner *v1alpha1.LocalModelNode) error {
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

	initSecurityContext := &corev1.SecurityContext{
		RunAsUser:                &rootUser,
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Add: []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER"},
		},
	}

	env := []corev1.EnvVar{
		{Name: "FIX_UID", Value: strconv.FormatInt(uid, 10)},
		{Name: "FIX_GID", Value: strconv.FormatInt(gid, 10)},
		{Name: "TARGET", Value: MountPath},
	}

	// The cache tree is relabeled recursively to the shared, category-less MCS level so it is
	// readable from any consuming namespace's MCS context (see sharedMCSLevel). The whole
	// MountPath is relabeled — MCS enforces "search" on parent directories, so a partial relabel
	// would leave an un-traversable categorized parent and reintroduce the read denial.
	//
	// The level is the sharedMCSLevel Go constant ("s0"), never interpolated with user data, so it
	// is safe to embed directly in the script. FIX_UID/FIX_GID/TARGET are passed via env.
	// SECURITY: double-quoting on variable expansions is critical — it prevents word splitting and
	// shell injection. Do not remove the quotes.
	script := `set -eu; chown -R "$FIX_UID:$FIX_GID" "$TARGET" && chcon -R -t container_file_t -l ` + sharedMCSLevel + ` "$TARGET"`

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
							Type: "spc_t",
							// spc_t bypasses MCS, so this level does not gate the relabel; it is set
							// to the shared level for determinism/hygiene now that the namespace MCS
							// no longer feeds this path.
							Level: sharedMCSLevel,
						},
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:            "fix-permissions",
							Image:           permissionFixImage,
							Command:         []string{"sh", "-c", script},
							Env:             env,
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
									Name:      constants.PvcSourceMountName,
									MountPath: MountPath,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: constants.PvcSourceMountName,
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

	if err := controllerutil.SetControllerReference(owner, job, c.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on permission fix job: %w", err)
	}

	createdJob, err := c.Clientset.BatchV1().Jobs(jobNamespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create permission fix job: %w", err)
	}
	c.Log.Info("Created permission fix job", "name", createdJob.Name, "node", nodeName)
	return nil
}
