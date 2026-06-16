package kservemodule

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	openshiftSCCMCSAnnotation       = "openshift.io/sa.scc.mcs"
	localModelNodeAgentDaemonSetName = "kserve-localmodelnode-agent"

	modelCacheReasonNamespaceMCSMissing = "NamespaceMCSMissing"
	modelCacheReasonSELinuxMCSMismatch  = "SELinuxMCSMismatch"
	modelCacheReasonResourcesNotReady   = "ResourcesNotReady"
)

// validMCSLevel matches openshift.io/sa.scc.mcs values (same rule as KServe platform_odh.go).
var validMCSLevel = regexp.MustCompile(`^s\d+(-s\d+)?(:c\d{1,4}([,.]c\d{1,4})*)?$`)

type modelCacheReadinessError struct {
	reason string
	msg    string
}

func (e *modelCacheReadinessError) Error() string {
	return e.msg
}

func newModelCacheReadinessError(reason, msg string) error {
	return &modelCacheReadinessError{reason: reason, msg: msg}
}

func modelCacheReadinessReason(err error) string {
	var readinessErr *modelCacheReadinessError
	if errors.As(err, &readinessErr) {
		return readinessErr.reason
	}
	return modelCacheReasonResourcesNotReady
}

func (r *KserveModuleReconciler) resolveNamespaceMCSLevel(ctx context.Context, namespace string) (string, error) {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return "", fmt.Errorf("getting namespace %q: %w", namespace, err)
	}

	mcs, ok := ns.Annotations[openshiftSCCMCSAnnotation]
	if !ok {
		return "", newModelCacheReadinessError(
			modelCacheReasonNamespaceMCSMissing,
			fmt.Sprintf("namespace %q is missing annotation %q", namespace, openshiftSCCMCSAnnotation),
		)
	}

	mcs = strings.TrimSpace(mcs)
	if mcs == "" {
		return "", newModelCacheReadinessError(
			modelCacheReasonNamespaceMCSMissing,
			fmt.Sprintf("namespace %q has empty annotation %q", namespace, openshiftSCCMCSAnnotation),
		)
	}

	if !validMCSLevel.MatchString(mcs) {
		return "", fmt.Errorf("invalid MCS level from namespace annotation: %q", mcs)
	}

	return mcs, nil
}

func patchLocalModelNodeAgentMCSLevel(resources []unstructured.Unstructured, mcsLevel string) ([]unstructured.Unstructured, error) {
	dsIdx, ds, err := getIndexedResource[appsv1.DaemonSet](resources, daemonSetGVK, localModelNodeAgentDaemonSetName)
	if err != nil {
		if errors.Is(err, errResourceNotFound) {
			return resources, nil
		}
		return nil, err
	}

	if ds.Spec.Template.Spec.SecurityContext == nil {
		ds.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	ds.Spec.Template.Spec.SecurityContext.SELinuxOptions = &corev1.SELinuxOptions{
		Level: mcsLevel,
	}

	return replaceResourceAtIndex(resources, dsIdx, ds)
}

func (r *KserveModuleReconciler) checkModelCacheDaemonSetMCS(ctx context.Context) error {
	ds := &appsv1.DaemonSet{}
	key := client.ObjectKey{
		Name:      localModelNodeAgentDaemonSetName,
		Namespace: r.getApplicationsNamespace(),
	}
	if err := r.Get(ctx, key, ds); err != nil {
		if k8serr.IsNotFound(err) {
			return newModelCacheReadinessError(
				modelCacheReasonResourcesNotReady,
				fmt.Sprintf("DaemonSet %s not found", localModelNodeAgentDaemonSetName),
			)
		}
		return fmt.Errorf("DaemonSet %s: %w", localModelNodeAgentDaemonSetName, err)
	}

	expectedMCS, err := r.resolveNamespaceMCSLevel(ctx, r.getApplicationsNamespace())
	if err != nil {
		return err
	}

	actualMCS := ""
	if ds.Spec.Template.Spec.SecurityContext != nil && ds.Spec.Template.Spec.SecurityContext.SELinuxOptions != nil {
		actualMCS = ds.Spec.Template.Spec.SecurityContext.SELinuxOptions.Level
	}

	if actualMCS != expectedMCS {
		return newModelCacheReadinessError(
			modelCacheReasonSELinuxMCSMismatch,
			fmt.Sprintf(
				`DaemonSet MCS level %q does not match namespace MCS %q`,
				actualMCS,
				expectedMCS,
			),
		)
	}

	return nil
}
