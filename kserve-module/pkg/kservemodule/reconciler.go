package kservemodule

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves,verbs=list;watch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves,resourceNames=default-kserve,verbs=get;update;patch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves/status,resourceNames=default-kserve,verbs=get;update;patch
//
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get

type KserveModuleReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ManifestsPath string

	workDir               string
	initDone              bool
	applicationsNamespace string
	clusterType           *cluster.ClusterType
}

func (r *KserveModuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	kserve := &platformv1alpha1.Kserve{}
	if err := r.Get(ctx, req.NamespacedName, kserve); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	mgmtState := platformv1alpha1.GetManagementState(kserve)
	log.Info("reconciling Kserve CR", "name", kserve.Name, "managementState", mgmtState)

	if mgmtState == common.Removed {
		// TODO(RHOAIENG-61119): run garbage collection to remove deployed resources,
		// then update status. Currently no-op because deploy is not yet implemented.
		log.Info("Kserve is Removed, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	componentErrors := r.reconcile(ctx, kserve)
	if len(componentErrors) > 0 {
		var msgs []string
		for name, err := range componentErrors {
			log.Error(err, "component reconciliation failed", "component", name)
			msgs = append(msgs, name+": "+err.Error())
		}
		return ctrl.Result{}, fmt.Errorf("reconciliation failed: %s", strings.Join(msgs, "; "))
	}

	return ctrl.Result{}, nil
}

func (r *KserveModuleReconciler) reconcile(ctx context.Context, kserve *platformv1alpha1.Kserve) map[string]error {
	manifestDir, err := r.ensureWorkDir()
	if err != nil {
		errs := make(map[string]error, len(components))
		for _, comp := range components {
			errs[comp.name] = fmt.Errorf("preparing writable manifests: %w", err)
		}
		return errs
	}

	componentErrors := make(map[string]error, len(components))
	for _, comp := range components {
		if err := r.reconcileComponent(ctx, kserve, manifestDir, comp); err != nil {
			componentErrors[comp.name] = err
		}
	}

	return componentErrors
}

func (r *KserveModuleReconciler) reconcileComponent(ctx context.Context,
	kserve *platformv1alpha1.Kserve, manifestDir string, comp componentConfig) error {

	log := ctrl.LoggerFrom(ctx)

	sourcePath := comp.sourcePath
	if comp.name == kserveComponentName && r.isKubernetes(ctx) {
		sourcePath = kserveManifestSourcePathXKS
	}

	if err := applyParams(
		filepath.Join(manifestDir, comp.name, sourcePath),
		comp.imageMap,
	); err != nil {
		return fmt.Errorf("applying %s image params: %w", comp.name, err)
	}

	if comp.name == kserveComponentName && r.isKubernetes(ctx) {
		ns := r.getApplicationsNamespace()
		if err := applyParams(
			filepath.Join(manifestDir, comp.name, kserveManifestSourcePathXKS),
			nil, buildCertManagerParams(ns),
		); err != nil {
			return fmt.Errorf("applying cert-manager params: %w", err)
		}
	}

	if comp.extraParams != nil {
		extra := comp.extraParams(kserve)
		if err := applyParams(
			filepath.Join(manifestDir, comp.name, sourcePath),
			nil, extra,
		); err != nil {
			return fmt.Errorf("applying %s extra params: %w", comp.name, err)
		}
	}

	renderPath := filepath.Join(manifestDir, comp.name, sourcePath)
	resources, err := kustomize.Render(renderPath, nil)
	if err != nil {
		return fmt.Errorf("rendering %s kustomize: %w", comp.name, err)
	}
	log.Info("rendered kustomize manifests", "component", comp.name, "resourceCount", len(resources))

	if comp.postRender != nil {
		resources, err = comp.postRender(ctx, r, kserve, resources)
		if err != nil {
			return fmt.Errorf("%s post-render: %w", comp.name, err)
		}
	}

	commonPostRender(resources, comp.name)

	log.Info("component reconciliation complete", "component", comp.name, "resources", len(resources))
	return nil
}

func (r *KserveModuleReconciler) isKubernetes(ctx context.Context) bool {
	if r.clusterType != nil {
		return *r.clusterType == cluster.ClusterTypeKubernetes
	}

	ct, err := cluster.DetectClusterType(ctx, r.Client)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to detect cluster type, assuming OpenShift")
		return false
	}

	r.clusterType = &ct
	return ct == cluster.ClusterTypeKubernetes
}

func (r *KserveModuleReconciler) getApplicationsNamespace() string {
	if r.applicationsNamespace != "" {
		return r.applicationsNamespace
	}

	if ns := os.Getenv("APPLICATIONS_NAMESPACE"); ns != "" {
		r.applicationsNamespace = ns
		return ns
	}

	return "opendatahub"
}

// getVersionPrefix returns a dash-separated version string (e.g. "v3-4-0")
// used to prefix LLMInferenceServiceConfig names so multiple versions can
// coexist during upgrades without name collisions.
//
// Resolution order:
// 1. platform.opendatahub.io/version annotation on Kserve CR (set by orchestrator)
// 2. PlatformContext.Release.Version projected into Kserve CR spec (future, when common.Release is in shared lib)
// 3. RELEASE_VERSION env var (injected by build pipeline)
// 4. Fallback "v0-0-0" for local development
func (r *KserveModuleReconciler) getVersionPrefix(kserve *platformv1alpha1.Kserve) string {
	// implementated based on current KSERVE CR
	if ann := kserve.GetAnnotations(); ann != nil {
		if v := ann["platform.opendatahub.io/version"]; v != "" {
			return "v" + strings.ReplaceAll(v, ".", "-")
		}
	}
	if v := os.Getenv("RELEASE_VERSION"); v != "" {
		return "v" + strings.ReplaceAll(v, ".", "-")
	}
	return "v0-0-0"
}

func (r *KserveModuleReconciler) ensureWorkDir() (string, error) {
	if r.initDone && r.workDir != "" {
		return r.workDir, nil
	}

	workDir := "/opt/manifests"
	srcDir := r.ManifestsPath
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		dst := filepath.Join(workDir, rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("copying manifests to workdir: %w", err)
	}

	r.workDir = workDir
	r.initDone = true
	return workDir, nil
}
