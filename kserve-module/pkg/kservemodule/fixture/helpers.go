package fixture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func ReadyDeployment(name, namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "main", Image: "busybox:latest"},
					},
				},
			},
		},
	}
}

func FindCondition(cr *platformv1alpha1.Kserve, condType string) *common.Condition {
	for i := range cr.Status.Conditions {
		if cr.Status.Conditions[i].Type == condType {
			return &cr.Status.Conditions[i]
		}
	}
	return nil
}

func CreateCRD(ctx context.Context, cli client.Client, group, version, kind string, scope apiextensionsv1.ResourceScope) *apiextensionsv1.CustomResourceDefinition {
	plural := strings.ToLower(kind) + "s"
	return createCRDInternal(ctx, cli, fmt.Sprintf("%s.%s", plural, group), group, version, plural, strings.ToLower(kind), kind, scope)
}

// CreateCRDByName creates a CRD from its full name (e.g. "authorizationpolicies.security.istio.io").
// The naive singular derivation (strip trailing "s") is incorrect for irregular plurals
// but harmless — dependency checks look up CRDs by full name, not by singular/kind.
func CreateCRDByName(ctx context.Context, cli client.Client, crdName, group, version string, scope apiextensionsv1.ResourceScope) *apiextensionsv1.CustomResourceDefinition {
	parts := strings.SplitN(crdName, ".", 2)
	gomega.ExpectWithOffset(1, len(parts) == 2 && parts[0] != "" && parts[1] != "").To(gomega.BeTrue(),
		"invalid CRD name %q; expected <plural>.<group>", crdName)
	plural := parts[0]
	singular, _ := strings.CutSuffix(plural, "s")
	return createCRDInternal(ctx, cli, crdName, group, version, plural, singular, singular, scope)
}

func createCRDInternal(ctx context.Context, cli client.Client, name, group, version, plural, singular, kind string, scope apiextensionsv1.ResourceScope) *apiextensionsv1.CustomResourceDefinition {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: version, Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"status": {Type: "object", XPreserveUnknownFields: ptr.To(true)},
						},
					},
				},
				Subresources: &apiextensionsv1.CustomResourceSubresources{
					Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
				},
			}},
			Scope: scope,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   plural,
				Singular: singular,
				Kind:     kind,
			},
		},
	}

	gomega.ExpectWithOffset(2, client.IgnoreAlreadyExists(cli.Create(ctx, crd))).To(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		var updated apiextensionsv1.CustomResourceDefinition
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: crd.Name}, &updated)).To(gomega.Succeed())
		for _, c := range updated.Status.Conditions {
			if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
				return
			}
		}
		g.Expect(false).To(gomega.BeTrue(), "CRD %s not established", crd.Name)
	}).WithContext(ctx).WithTimeout(30 * time.Second).Should(gomega.Succeed())

	return crd
}

func CreateSubscription(ctx context.Context, cli client.Client, name, namespace string) *unstructured.Unstructured {
	sub := &unstructured.Unstructured{}
	sub.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription",
	})
	sub.SetName(name)
	sub.SetNamespace(namespace)
	sub.Object["spec"] = map[string]any{
		"channel": "stable",
		"name":    name,
		"source":  "test-catalog",
	}

	gomega.ExpectWithOffset(1, cli.Create(ctx, sub)).To(gomega.Succeed())
	return sub
}

func ProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("failed to get working directory: %v", err))
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("go.mod not found")
		}
		dir = parent
	}
}
