package kservemodule

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves,verbs=list;watch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves,resourceNames=default-kserve,verbs=get;update;patch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves/status,resourceNames=default-kserve,verbs=get;update;patch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=kserves/finalizers,resourceNames=default-kserve,verbs=update
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get
// +kubebuilder:rbac:groups="",resources=configmaps;services;serviceaccounts,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;delete;patch;update,resourceNames=kserve-webhook-server-secret
// +kubebuilder:rbac:groups="",resources=configmaps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=create;delete;get;list;patch;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=create;delete;get;list;patch;update;watch
// escalate/bind scoped to the exact roles and clusterroles deployed by this controller
// +kubebuilder:rbac:groups=nim.opendatahub.io,resources=accounts,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=nim.opendatahub.io,resources=accounts/finalizers,verbs=get;update
// +kubebuilder:rbac:groups=nim.opendatahub.io,resources=accounts/status,verbs=get;update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind;escalate,resourceNames=account-editor-role;account-viewer-role;kserve-admin;kserve-edit;kserve-view;kserve-manager-role;kserve-proxy-role;kserve-llmisvc-manager-role;kserve-llmisvc-distro-role;kserve-metrics-reader-cluster-role;openshift-ai-llminferenceservice-scc;odh-model-controller-role;proxy-role;model-serving-api;metrics-reader;kserve-prometheus-k8s
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=bind;escalate,resourceNames=kserve-leader-election-role;llmisvc-leader-election-role;leader-election-role
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles/finalizers;rolebindings/finalizers;clusterroles/finalizers;clusterrolebindings/finalizers,verbs=update
// no delete — CRDs survive CR deletion
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=create;get;list;patch;update;watch
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates;issuers,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=cert-manager.io,resources=clusterissuers,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations;validatingwebhookconfigurations,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=serving.kserve.io,resources=llminferenceserviceconfigs;clusterstoragecontainers,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=template.openshift.io,resources=templates,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=operators.coreos.com,resources=subscriptions,verbs=get;list;watch
// +kubebuilder:rbac:groups=operator.openshift.io,resources=leaderworkersets,verbs=get;list;watch

type ResourceDeployer interface {
	Deploy(ctx context.Context, input deploy.DeployInput) error
}

type KserveModuleReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	ManifestsTemplatePath string
	Deployer              ResourceDeployer

	workDir               string
	initDone              bool
	applicationsNamespace string
	clusterType           *cluster.ClusterType

	controller     controller.Controller
	cache          cache.Cache
	dynamicWatches []*dynamicWatch
	dynamicWatchMu sync.Mutex
}

func (r *KserveModuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := ctrl.LoggerFrom(ctx)

	kserve := &platformv1alpha1.Kserve{}
	if err := r.Get(ctx, req.NamespacedName, kserve); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling Kserve CR", "name", kserve.Name)

	r.registerDynamicWatches(ctx)

	condMgr := newConditionManager(kserve)
	defer func() {
		if err := r.updateStatus(ctx, kserve, condMgr); err != nil && retErr == nil {
			retErr = err
		}
	}()

	depResult := r.checkDependencies(ctx)
	applyDependencyConditions(condMgr, depResult)
	if len(depResult.criticalErrors) > 0 {
		applyProvisioningCondition(condMgr, map[string]error{
			"dependencies": fmt.Errorf("critical dependencies unavailable"),
		})
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	componentErrors := r.reconcile(ctx, kserve)
	applyProvisioningCondition(condMgr, componentErrors)
	if len(componentErrors) > 0 {
		var msgs []string
		for _, name := range slices.Sorted(maps.Keys(componentErrors)) {
			log.Error(componentErrors[name], "component reconciliation failed", "component", name)
			msgs = append(msgs, name+": "+componentErrors[name].Error())
		}
		return ctrl.Result{}, fmt.Errorf("reconciliation failed: %s", strings.Join(msgs, "; "))
	}

	r.updateComponentReadiness(ctx, condMgr)

	if !condMgr.IsHappy() {
		log.Info("not all components ready, requeueing")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (r *KserveModuleReconciler) reconcile(ctx context.Context, kserve *platformv1alpha1.Kserve) map[string]error {
	log := ctrl.LoggerFrom(ctx)

	manifestDir, err := r.ensureWorkDir()
	if err != nil {
		errs := make(map[string]error, len(components))
		for _, comp := range components {
			errs[comp.name] = fmt.Errorf("preparing writable manifests: %w", err)
		}
		return errs
	}

	var allResources []unstructured.Unstructured
	componentErrors := make(map[string]error, len(components))

	for _, comp := range components {
		resources, err := r.reconcileComponent(ctx, kserve, manifestDir, comp)
		if err != nil {
			componentErrors[comp.name] = err
			continue
		}
		allResources = append(allResources, resources...)
	}

	if len(componentErrors) > 0 {
		return componentErrors
	}

	if err := r.Deployer.Deploy(ctx, deploy.DeployInput{
		Client:    r.Client,
		Owner:     kserve,
		Resources: allResources,
	}); err != nil {
		return map[string]error{"deploy": fmt.Errorf("applying resources: %w", err)}
	}

	log.Info("deployed all resources", "count", len(allResources))

	return nil
}

func (r *KserveModuleReconciler) reconcileComponent(ctx context.Context,
	kserve *platformv1alpha1.Kserve, manifestDir string, comp componentConfig) ([]unstructured.Unstructured, error) {

	log := ctrl.LoggerFrom(ctx)

	sourcePath := comp.sourcePath
	if r.isKubernetes(ctx) {
		if comp.sourcePathXKS == "" {
			log.Info("no XKS overlay, skipping component", "component", comp.name)
			return nil, nil
		}
		sourcePath = comp.sourcePathXKS
	}

	if err := applyParams(
		filepath.Join(manifestDir, comp.name, sourcePath),
		comp.imageMap,
	); err != nil {
		return nil, fmt.Errorf("applying %s image params: %w", comp.name, err)
	}

	if r.isKubernetes(ctx) {
		ns := r.getApplicationsNamespace()
		if err := applyParams(
			filepath.Join(manifestDir, comp.name, comp.sourcePathXKS),
			nil, buildCertManagerParams(ns),
		); err != nil {
			return nil, fmt.Errorf("applying cert-manager params: %w", err)
		}
	}

	if comp.extraParams != nil {
		extra := comp.extraParams(kserve)
		if err := applyParams(
			filepath.Join(manifestDir, comp.name, sourcePath),
			nil, extra,
		); err != nil {
			return nil, fmt.Errorf("applying %s extra params: %w", comp.name, err)
		}
	}

	renderPath := filepath.Join(manifestDir, comp.name, sourcePath)
	resources, err := kustomize.Render(renderPath, nil)
	if err != nil {
		return nil, fmt.Errorf("rendering %s kustomize: %w", comp.name, err)
	}
	log.Info("rendered kustomize manifests", "component", comp.name, "resourceCount", len(resources))

	if comp.postRender != nil {
		resources, err = comp.postRender(ctx, r, kserve, resources)
		if err != nil {
			return nil, fmt.Errorf("%s post-render: %w", comp.name, err)
		}
	}

	commonPostRender(resources, kserveComponentName)

	log.Info("component rendering complete", "component", comp.name, "resources", len(resources))
	return resources, nil
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

// TODO: for now, we can use the annotation but the version will be set by data of configmap
// We need to confirm with platform team what configmap name is used for the version.
func (r *KserveModuleReconciler) getVersionPrefix(kserve *platformv1alpha1.Kserve) string {
	if ann := kserve.GetAnnotations(); ann != nil {
		if v := ann["platform.opendatahub.io/version"]; v != "" {
			return "v" + strings.ReplaceAll(v, ".", "-")
		}
	}
	return "v0-0-0"
}

func (r *KserveModuleReconciler) WorkDir() string {
	return r.workDir
}

func (r *KserveModuleReconciler) SetClusterType(ct cluster.ClusterType) {
	r.clusterType = &ct
}

func (r *KserveModuleReconciler) SetWorkDir(dir string) {
	r.workDir = dir
	r.initDone = true
}

func (r *KserveModuleReconciler) ensureWorkDir() (string, error) {
	if r.initDone && r.workDir != "" {
		return r.workDir, nil
	}

	workDir := "/opt/manifests"
	srcDir := r.ManifestsTemplatePath
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

