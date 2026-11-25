package kservemodule

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

func TestModelCacheRenderedNamespaceReferences(t *testing.T) {
	r := newReconcilerWithFakeClient()
	path := filepath.Join("..", "..", "..", "config", "overlays", "odh-modelcache")
	resources, err := kustomize.Render(path, nil, kustomize.WithNamespace(modelCacheNamespace))
	if err != nil {
		t.Fatal(err)
	}
	resources, err = modelCacheComponentPostRender(context.Background(), r, nil, resources)
	if err != nil {
		t.Fatal(err)
	}
	_, deployment, err := getIndexedResource[appsv1.Deployment](resources, deploymentGVK, localmodelControllerDeployment)
	if err != nil {
		t.Fatal(err)
	}
	_, daemonSet, err := getIndexedResource[appsv1.DaemonSet](resources, daemonSetGVK, localModelNodeAgentDaemonSetName)
	if err != nil {
		t.Fatal(err)
	}
	for _, workload := range []struct {
		name       string
		namespace  string
		containers []corev1.Container
	}{
		{deployment.Name, deployment.Namespace, deployment.Spec.Template.Spec.Containers},
		{daemonSet.Name, daemonSet.Namespace, daemonSet.Spec.Template.Spec.Containers},
	} {
		if workload.namespace != modelCacheNamespace {
			t.Fatalf("%s rendered in %s", workload.name, workload.namespace)
		}
		manager := workload.containers[0]
		var configNamespace, podNamespace string
		for _, env := range manager.Env {
			switch env.Name {
			case kserveConfigNamespaceEnv:
				configNamespace = env.Value
			case "POD_NAMESPACE":
				if env.ValueFrom != nil && env.ValueFrom.FieldRef != nil {
					podNamespace = env.ValueFrom.FieldRef.FieldPath
				}
			}
		}
		if configNamespace != "test-ns" || podNamespace != "metadata.namespace" {
			t.Fatalf("%s has wrong namespace environment: config=%q pod=%q", workload.name, configNamespace, podNamespace)
		}
	}
	for _, tt := range []struct{ kind, name, namespace string }{
		{"PersistentVolumeClaim", modelCachePVCName, modelCacheNamespace},
		{"Service", "localmodel-webhook-server-service", modelCacheNamespace},
		{"Role", "kserve-localmodel-ca-bundle", modelCacheNamespace},
		{"RoleBinding", "kserve-localmodel-ca-bundle", modelCacheNamespace},
		{"Role", "kserve-localmodelnode-ca-bundle-reader", modelCacheNamespace},
		{"RoleBinding", "kserve-localmodelnode-ca-bundle-reader", modelCacheNamespace},
		{"Role", "kserve-modelcache-configmap-source-reader", "test-ns"},
		{"RoleBinding", "kserve-modelcache-configmap-source-reader", "test-ns"},
		{"Certificate", "kernelcache-root-ca", modelCacheNamespace},
		{"Certificate", "kernelcache-signer", modelCacheNamespace},
		{"Issuer", "kernelcache-ca", modelCacheNamespace},
	} {
		res := findRenderedModelCacheResource(resources, tt.kind, tt.name)
		if res == nil || res.GetNamespace() != tt.namespace {
			t.Fatalf("%s %s expected namespace %s, got %#v", tt.kind, tt.name, tt.namespace, res)
		}
	}
	sourceReader := findRenderedModelCacheResource(resources, "RoleBinding", "kserve-modelcache-configmap-source-reader")
	subjects, ok := sourceReader.Object["subjects"].([]any)
	if !ok || len(subjects) != 2 {
		t.Fatalf("source ConfigMap RoleBinding has unexpected subjects: %#v", sourceReader.Object["subjects"])
	}
	for _, subject := range subjects {
		if subject.(map[string]any)["namespace"] != modelCacheNamespace {
			t.Fatalf("source ConfigMap RoleBinding points to wrong service account namespace: %#v", subject)
		}
	}
	for _, name := range []string{
		"kserve-localmodel-distro-role",
		"kserve-localmodelnode-distro-role",
		"kserve-localmodelnode-agent-role",
	} {
		role := findRenderedModelCacheResource(resources, "ClusterRole", name)
		if role == nil {
			t.Fatalf("ClusterRole %s missing", name)
		}
		for _, rule := range role.Object["rules"].([]any) {
			for _, resource := range rule.(map[string]any)["resources"].([]any) {
				if resource == "configmaps" {
					t.Fatalf("ClusterRole %s still grants cluster-wide ConfigMap access", name)
				}
			}
		}
	}
	for _, name := range []string{
		"kserve-localmodel-manager-rolebinding",
		"kserve-localmodel-distro-rolebinding",
		"kserve-localmodelnode-agent-rolebinding",
		"kserve-localmodelnode-distro-rolebinding",
		"kserve-localmodel-permfix-rolebinding",
		"kserve-kernelcache-nodegroup-manager",
	} {
		res := findRenderedModelCacheResource(resources, "ClusterRoleBinding", name)
		if res == nil {
			t.Fatalf("ClusterRoleBinding %s missing", name)
		}
		subjects, ok := res.Object["subjects"].([]any)
		if !ok || len(subjects) != 1 {
			t.Fatalf("ClusterRoleBinding %s has unexpected subjects: %#v", name, res.Object["subjects"])
		}
		subject, ok := subjects[0].(map[string]any)
		if !ok || subject["namespace"] != modelCacheNamespace {
			t.Fatalf("ClusterRoleBinding %s subject namespace is %#v", name, subject)
		}
	}
}

// Render both components through the module namespace transformer: the main
// overlay's TLS binding cannot grant access to accounts in the cache namespace.
func TestModelCacheRenderedStartupPermissions(t *testing.T) {
	var resources []unstructured.Unstructured
	for _, overlay := range []struct{ path, namespace string }{
		{"odh", "custom-applications"},
		{"odh-modelcache", modelCacheNamespace},
	} {
		path := filepath.Join("..", "..", "..", "config", "overlays", overlay.path)
		rendered, err := kustomize.Render(path, nil, kustomize.WithNamespace(overlay.namespace))
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, rendered...)
	}
	for _, account := range []string{"kserve-localmodel-controller-manager", "kserve-localmodelnode-agent"} {
		for _, verb := range []string{"get", "list", "watch"} {
			assertRenderedClusterPermission(t, resources, account, "config.openshift.io", "apiservers", "cluster", verb)
		}
	}
	assertRenderedClusterPermission(t, resources, "kserve-localmodelnode-agent", "security.openshift.io", "securitycontextconstraints", "openshift-ai-localmodel-scc", "use")
	assertRenderedClusterPermission(t, resources, "kserve-localmodel-permfix", "security.openshift.io", "securitycontextconstraints", "kserve-localmodel-permissions-scc", "use")

	mainBinding := findRenderedModelCacheResource(resources, "ClusterRoleBinding", "kserve-tls-distro-rolebinding")
	binding := &rbacv1.ClusterRoleBinding{}
	if mainBinding == nil {
		t.Fatal("main TLS binding missing")
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(mainBinding.Object, binding); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"kserve-controller-manager", "llmisvc-controller-manager"} {
		if !slices.Contains(binding.Subjects, rbacv1.Subject{Kind: "ServiceAccount", Name: account, Namespace: "custom-applications"}) {
			t.Fatalf("main TLS binding lost applications-namespace subject %s", account)
		}
	}

	// The dedicated agent SCC must permit the workload's fixed UID/group without
	// allowing arbitrary SELinux labels or privileged containers.
	scc := findRenderedModelCacheResource(resources, "SecurityContextConstraints", "openshift-ai-localmodel-scc")
	if scc == nil {
		t.Fatal("agent SCC missing")
	}
	_, agent, err := getIndexedResource[appsv1.DaemonSet](resources, daemonSetGVK, localModelNodeAgentDaemonSetName)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Spec.Template.Spec.ServiceAccountName != "kserve-localmodelnode-agent" {
		t.Fatalf("unexpected agent service account %q", agent.Spec.Template.Spec.ServiceAccountName)
	}
	for _, field := range []string{"allowPrivilegedContainer", "allowPrivilegeEscalation"} {
		allowed, found, err := unstructured.NestedBool(scc.Object, field)
		if err != nil || !found || allowed {
			t.Fatalf("agent SCC must explicitly forbid %s: %v", field, err)
		}
	}
	security := agent.Spec.Template.Spec.SecurityContext
	if security == nil || security.RunAsUser == nil || security.FSGroup == nil {
		t.Fatal("agent security context must specify UID and fsGroup")
	}
	uid, _, err := unstructured.NestedInt64(scc.Object, "runAsUser", "uid")
	if err != nil || uid != *security.RunAsUser {
		t.Fatalf("agent SCC UID %d does not match workload UID %d: %v", uid, *security.RunAsUser, err)
	}
	for _, field := range []string{"runAsUser", "fsGroup", "seLinuxContext"} {
		strategy, _, err := unstructured.NestedString(scc.Object, field, "type")
		if err != nil || strategy != "MustRunAs" {
			t.Fatalf("agent SCC %s strategy must be MustRunAs, got %q: %v", field, strategy, err)
		}
	}
	ranges, _, err := unstructured.NestedSlice(scc.Object, "fsGroup", "ranges")
	if err != nil || len(ranges) != 1 {
		t.Fatalf("unexpected agent SCC group ranges: %v: %v", ranges, err)
	}
	groupRange := ranges[0].(map[string]any)
	if groupRange["min"] != *security.FSGroup || groupRange["max"] != *security.FSGroup {
		t.Fatalf("agent SCC must permit exactly workload fsGroup %d, got %v", *security.FSGroup, groupRange)
	}
}

// TestModelCacheRenderedDownloadEgressNetworkPolicy pins the static egress-only
// NetworkPolicy `kserve-localmodel-download` that the odh-modelcache overlay ships for
// per-node model download Jobs. The manifest owns this policy's lifecycle: no controller
// creates, watches, or cleans it, so this test keeps the rendered namespace, selector,
// and spec stable. The selector literals are asserted as plain strings because
// kserve-module cannot import the root module's Go constants
// (constants.DownloadJobLabelKey / constants.DownloadJobLabelValueNode in
// pkg/constants/constants_odh.go in the kserve repo); the root-package test
// pkg/controller/v1alpha1/localmodelnode/platform_odh_test.go pins those constants to
// the same literals, keeping manifest and controller code in sync.
func TestModelCacheRenderedDownloadEgressNetworkPolicy(t *testing.T) {
	r := newReconcilerWithFakeClient()
	path := filepath.Join("..", "..", "..", "config", "overlays", "odh-modelcache")
	resources, err := kustomize.Render(path, nil, kustomize.WithNamespace(modelCacheNamespace))
	if err != nil {
		t.Fatal(err)
	}
	resources, err = modelCacheComponentPostRender(context.Background(), r, nil, resources)
	if err != nil {
		t.Fatal(err)
	}
	rendered := findRenderedModelCacheResource(resources, "NetworkPolicy", "kserve-localmodel-download")
	if rendered == nil {
		t.Fatal("NetworkPolicy kserve-localmodel-download missing from rendered overlay")
	}
	policy := &netv1.NetworkPolicy{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rendered.Object, policy); err != nil {
		t.Fatal(err)
	}
	if policy.Namespace != modelCacheNamespace {
		t.Fatalf("kserve-localmodel-download rendered in namespace %q, want %q", policy.Namespace, modelCacheNamespace)
	}
	selector := policy.Spec.PodSelector.MatchLabels
	if len(selector) != 1 || selector["serving.kserve.io/localmodel-download"] != "node" ||
		len(policy.Spec.PodSelector.MatchExpressions) != 0 {
		t.Fatalf("kserve-localmodel-download selector = %+v, want exactly serving.kserve.io/localmodel-download: node",
			policy.Spec.PodSelector)
	}
	if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != netv1.PolicyTypeEgress {
		t.Fatalf("kserve-localmodel-download policyTypes = %v, want [Egress] only", policy.Spec.PolicyTypes)
	}
	// Egress-only by design: no ingress rules are added, so download Job pods keep the
	// namespace's default ingress isolation.
	if len(policy.Spec.Ingress) != 0 {
		t.Fatalf("kserve-localmodel-download must add no ingress rules, got %v", policy.Spec.Ingress)
	}
	// Exactly one empty egress rule: an explicit allow-all grant over every destination,
	// protocol, and port, so configurable storage/registry/authentication/DNS endpoints
	// all work even under namespace default-deny egress.
	if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 0 || len(policy.Spec.Egress[0].Ports) != 0 {
		t.Fatalf("kserve-localmodel-download egress = %+v, want exactly one empty allow-all rule", policy.Spec.Egress)
	}
}

// TestModelCacheRenderedNetworkPolicyRBAC verifies the generated LocalModel RBAC behind
// the cache-owned import egress NetworkPolicies (see
// pkg/controller/v1alpha1/localmodel/reconcilers/sharedpvc_platform_odh.go in the kserve
// repo): only the LocalModel manager role may touch networkpolicies, with exactly the
// reconcile verbs get/list/watch/create/update. No patch or delete — teardown is
// owner-reference garbage collection with the cache CR — and the node agent gains no
// networkpolicy privileges. The static overlay policy needs no controller RBAC at all.
func TestModelCacheRenderedNetworkPolicyRBAC(t *testing.T) {
	r := newReconcilerWithFakeClient()
	path := filepath.Join("..", "..", "..", "config", "overlays", "odh-modelcache")
	resources, err := kustomize.Render(path, nil, kustomize.WithNamespace(modelCacheNamespace))
	if err != nil {
		t.Fatal(err)
	}
	resources, err = modelCacheComponentPostRender(context.Background(), r, nil, resources)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		want []string
	}{
		{"kserve-localmodel-distro-role", []string{"create", "get", "list", "update", "watch"}},
		{"kserve-localmodelnode-distro-role", nil},
		{"kserve-localmodelnode-agent-role", nil},
	} {
		rendered := findRenderedModelCacheResource(resources, "ClusterRole", tt.name)
		if rendered == nil {
			t.Fatalf("ClusterRole %s missing", tt.name)
		}
		role := &rbacv1.ClusterRole{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rendered.Object, role); err != nil {
			t.Fatal(err)
		}
		var verbs []string
		for _, rule := range role.Rules {
			if slices.Contains(rule.APIGroups, "networking.k8s.io") && slices.Contains(rule.Resources, "networkpolicies") {
				verbs = append(verbs, rule.Verbs...)
			}
		}
		slices.Sort(verbs)
		if !slices.Equal(verbs, tt.want) {
			t.Fatalf("ClusterRole %s networkpolicies verbs = %v, want %v", tt.name, verbs, tt.want)
		}
	}
}

func assertRenderedClusterPermission(t *testing.T, resources []unstructured.Unstructured, account, group, resource, name, verb string) {
	t.Helper()
	for i := range resources {
		if resources[i].GetKind() != "ClusterRoleBinding" {
			continue
		}
		binding := &rbacv1.ClusterRoleBinding{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(resources[i].Object, binding); err != nil {
			t.Fatal(err)
		}
		subject := rbacv1.Subject{Kind: "ServiceAccount", Name: account, Namespace: modelCacheNamespace}
		if !slices.Contains(binding.Subjects, subject) || binding.RoleRef.Kind != "ClusterRole" {
			continue
		}
		renderedRole := findRenderedModelCacheResource(resources, "ClusterRole", binding.RoleRef.Name)
		if renderedRole == nil {
			t.Fatalf("binding %s references missing role %s", binding.Name, binding.RoleRef.Name)
		}
		role := &rbacv1.ClusterRole{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(renderedRole.Object, role); err != nil {
			t.Fatal(err)
		}
		for _, rule := range role.Rules {
			if slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, resource) && slices.Contains(rule.Verbs, verb) &&
				(len(rule.ResourceNames) == 0 || slices.Contains(rule.ResourceNames, name)) {
				return
			}
		}
	}
	t.Fatalf("service account %s/%s lacks %s on %s/%s %s", modelCacheNamespace, account, verb, group, resource, name)
}

func findRenderedModelCacheResource(resources []unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for i := range resources {
		if resources[i].GetKind() == kind && resources[i].GetName() == name {
			return &resources[i]
		}
	}
	return nil
}
