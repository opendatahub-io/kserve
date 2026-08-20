package kservemodule

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

// deletionRequeueInterval is the fallback re-check interval for a blocked
// deletion; watches drive most re-checks, this re-reads if an event is missed.
const deletionRequeueInterval = 30 * time.Second

// configCleanupOutcome reports the result of a well-known config cleanup pass.
// When done is false the deletion is blocked; blockers describes why.
type configCleanupOutcome struct {
	// done is true when no well-known config remains.
	done bool
	// blockers describes what is holding deletion, for the status message.
	blockers []string
}

// cleanupLLMISVCConfigsOnDelete deletes the well-known LLMInferenceServiceConfigs
// during Kserve CR deletion. Configs still referenced by an LLMInferenceService
// (per status.referencedBy) are left in place and reported as blockers.
//
// Deleting a well-known config requires the llmisvc controller to be available:
// its validating webhook gates the delete (failurePolicy=Fail), and the ODH
// overlay ships PREVENT_WELL_KNOWN_CONFIG_DELETION=false so the webhook permits it.
func (r *KserveModuleReconciler) cleanupLLMISVCConfigsOnDelete(ctx context.Context) (configCleanupOutcome, error) {
	log := ctrl.LoggerFrom(ctx)
	ns := r.getApplicationsNamespace()

	configs, err := r.listWellKnownLLMISVCConfigs(ctx, ns)
	if err != nil {
		return configCleanupOutcome{}, err
	}
	if len(configs) == 0 {
		return configCleanupOutcome{done: true}, nil
	}

	controllerUnavailable := configCleanupOutcome{blockers: []string{"llmisvc controller is not available"}}
	dep := &appsv1.Deployment{}
	key := client.ObjectKey{Namespace: ns, Name: llmISVCControllerDeployment}
	if err := r.Get(ctx, key, dep); err != nil {
		if k8serr.IsNotFound(err) {
			return controllerUnavailable, nil
		}
		return configCleanupOutcome{}, fmt.Errorf("getting %s deployment: %w", llmISVCControllerDeployment, err)
	}
	if dep.Status.AvailableReplicas < 1 {
		return controllerUnavailable, nil
	}

	// check-before-delete: never touch a config that is still referenced.
	if blockers := referencedConfigBlockers(configs); len(blockers) > 0 {
		return configCleanupOutcome{blockers: blockers}, nil
	}

	if err := r.deleteWellKnownConfigs(ctx, configs); err != nil {
		return configCleanupOutcome{}, err
	}

	// Confirm they are actually gone; the config finalizer may still be running,
	// or a service could have appeared mid-flight.
	remaining, err := r.listWellKnownLLMISVCConfigs(ctx, ns)
	if err != nil {
		return configCleanupOutcome{}, err
	}
	if len(remaining) > 0 {
		if blockers := referencedConfigBlockers(remaining); len(blockers) > 0 {
			return configCleanupOutcome{blockers: blockers}, nil
		}
		log.Info("well-known configs still terminating, requeueing", "count", len(remaining))
		return configCleanupOutcome{blockers: []string{"waiting for well-known configs to finish terminating"}}, nil
	}

	return configCleanupOutcome{done: true}, nil
}

// listWellKnownLLMISVCConfigs returns the LLMInferenceServiceConfigs in ns that
// carry the well-known annotation.
func (r *KserveModuleReconciler) listWellKnownLLMISVCConfigs(ctx context.Context, ns string) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(llmISVCConfigListGVK)
	if err := r.List(ctx, list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("listing LLMInferenceServiceConfigs: %w", err)
	}

	var wellKnown []unstructured.Unstructured
	for i := range list.Items {
		if isWellKnownConfig(&list.Items[i]) {
			wellKnown = append(wellKnown, list.Items[i])
		}
	}
	return wellKnown, nil
}

func referencedConfigBlockers(configs []unstructured.Unstructured) []string {
	var blockers []string
	for i := range configs {
		refs, err := referencedByNames(&configs[i])
		if err != nil {
			// Fail safe: an unreadable status.referencedBy could be hiding a live
			// reference, so block deletion rather than delete a possibly in-use config.
			blockers = append(blockers, fmt.Sprintf("%s (status.referencedBy unreadable: %v)", configs[i].GetName(), err))
			continue
		}
		if len(refs) > 0 {
			blockers = append(blockers, fmt.Sprintf("%s (referenced by %s)", configs[i].GetName(), strings.Join(refs, ", ")))
		}
	}
	sort.Strings(blockers)
	return blockers
}

func referencedByNames(cfg *unstructured.Unstructured) ([]string, error) {
	refs, found, err := unstructured.NestedSlice(cfg.Object, "status", "referencedBy")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		m, ok := ref.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			// A referencedBy entry with no service name is malformed (partial or
			// garbage status). Skipping it avoids emitting bogus "ns/" blockers
			// that would wedge deletion on a reference that names nothing.
			continue
		}
		namespace, _ := m["namespace"].(string)
		if namespace == "" {
			// No namespace recorded; report the bare name rather than "/name".
			names = append(names, name)
		} else {
			names = append(names, namespace+"/"+name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (r *KserveModuleReconciler) deleteWellKnownConfigs(ctx context.Context, configs []unstructured.Unstructured) error {
	log := ctrl.LoggerFrom(ctx)
	for i := range configs {
		if !configs[i].GetDeletionTimestamp().IsZero() {
			continue // already terminating
		}
		if err := r.Delete(ctx, &configs[i]); err != nil && !k8serr.IsNotFound(err) {
			return fmt.Errorf("deleting LLMInferenceServiceConfig %s: %w", configs[i].GetName(), err)
		}
		log.Info("deleted well-known LLMInferenceServiceConfig", "name", configs[i].GetName())
	}
	return nil
}

// setDeletionBlocked records the Degraded/DeletionBlocked condition describing
// why the Kserve CR deletion cannot yet proceed. Callers pass a non-empty list
// of blockers (still-referenced configs, an unavailable llmisvc controller, or a
// component cleanup failure).
func (r *KserveModuleReconciler) setDeletionBlocked(ctx context.Context, kserve *platformv1alpha1.Kserve, blockers []string) error {
	condMgr := newConditionManager(kserve)
	condMgr.MarkTrue(string(common.ConditionTypeDegraded),
		conditions.WithSeverity(common.ConditionSeverityError),
		conditions.WithReason(ReasonDeletionBlocked),
		conditions.WithMessage("deletion blocked: %s", strings.Join(blockers, "; ")))
	return r.updateStatus(ctx, kserve, condMgr)
}
