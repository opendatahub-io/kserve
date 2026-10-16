package kservemodule

import (
	"context"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

func findRenderedModelCacheResource(resources []unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for i := range resources {
		if resources[i].GetKind() == kind && resources[i].GetName() == name {
			return &resources[i]
		}
	}
	return nil
}
