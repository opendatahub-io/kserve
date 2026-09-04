package kservemodule

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
// A dedicated validating webhook rejects deletion of well-known configs
// (failurePolicy=Fail), so once no config is referenced the webhook is removed
// before the configs are deleted. The webhook is owned by the Kserve CR and this
// deletion branch never re-applies operands, so it does not come back.
func (r *KserveModuleReconciler) cleanupLLMISVCConfigsOnDelete(ctx context.Context) (configCleanupOutcome, error) {
	ns := r.getApplicationsNamespace()

	configs, err := r.listWellKnownLLMISVCConfigs(ctx, ns)
	if err != nil {
		return configCleanupOutcome{}, err
	}
	if len(configs) == 0 {
		return configCleanupOutcome{done: true}, nil
	}

	// check-before-delete: skip deletion while a config looks referenced. This is
	// a best-effort early guard (the referencedBy read may be slightly stale); the
	// config's own finalizer is the authoritative guard that keeps an in-use config
	// alive even if a delete is issued.
	if blockers := referencedConfigBlockers(configs); len(blockers) > 0 {
		return configCleanupOutcome{blockers: blockers}, nil
	}

	// Nothing references the configs: remove the delete-guard webhook (not
	// restored for the rest of teardown), then delete the configs.
	if err := r.deleteConfigDeletionWebhook(ctx); err != nil {
		return configCleanupOutcome{}, err
	}

	if err := r.deleteWellKnownConfigs(ctx, configs); err != nil {
		return configCleanupOutcome{}, err
	}

	// Configs carry a finalizer, so they terminate asynchronously. Block for now;
	// the next reconcile re-lists from the top and reports done once they are gone
	// (or re-blocks if a reference reappeared meanwhile).
	return configCleanupOutcome{blockers: []string{"waiting for well-known configs to finish terminating"}}, nil
}

// listWellKnownLLMISVCConfigs returns the LLMInferenceServiceConfigs in ns that
// carry the well-known annotation.
func (r *KserveModuleReconciler) listWellKnownLLMISVCConfigs(ctx context.Context, ns string) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(llmISVCConfigListGVK)
	if err := r.List(ctx, list, client.InNamespace(ns)); err != nil {
		if meta.IsNoMatchError(err) {
			// CRD not installed: no configs can exist, so there is nothing to
			// clean up. (In production the module installs the CRD, so this only
			// spares deletion from wedging if the CRD is somehow absent.)
			return nil, nil
		}
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
		if err := r.Delete(ctx, &configs[i]); err != nil {
			if k8serr.IsNotFound(err) {
				continue // already gone
			}
			return fmt.Errorf("deleting LLMInferenceServiceConfig %s: %w", configs[i].GetName(), err)
		}
		log.Info("deleted well-known LLMInferenceServiceConfig", "name", configs[i].GetName())
	}
	return nil
}

// deleteConfigDeletionWebhook removes the dedicated ValidatingWebhookConfiguration
// that guards LLMInferenceServiceConfig deletion. It is idempotent: an already
// absent webhook is treated as success.
func (r *KserveModuleReconciler) deleteConfigDeletionWebhook(ctx context.Context) error {
	webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	webhook.SetName(llmISVCConfigWebhookName)
	err := r.Delete(ctx, webhook)
	if k8serr.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting %s ValidatingWebhookConfiguration: %w", llmISVCConfigWebhookName, err)
	}
	ctrl.LoggerFrom(ctx).Info("deleted config-deletion validating webhook", "name", llmISVCConfigWebhookName)
	return nil
}

// setDeletionBlocked records the Degraded/DeletionBlocked condition describing
// why the Kserve CR deletion cannot yet proceed. Callers pass a non-empty list
// of blockers (still-referenced configs, or a component cleanup failure).
func (r *KserveModuleReconciler) setDeletionBlocked(ctx context.Context, kserve *platformv1alpha1.Kserve, blockers []string) error {
	condMgr := newConditionManager(kserve)
	condMgr.MarkTrue(string(common.ConditionTypeDegraded),
		conditions.WithSeverity(common.ConditionSeverityError),
		conditions.WithReason(ReasonDeletionBlocked),
		conditions.WithMessage("deletion blocked: %s", strings.Join(blockers, "; ")))
	return r.updateStatus(ctx, kserve, condMgr)
}
