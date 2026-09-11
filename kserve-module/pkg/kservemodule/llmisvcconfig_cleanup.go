package kservemodule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

// deletionRequeueInterval is the fallback re-check interval for a blocked
// deletion; watches drive most re-checks, this re-reads if an event is missed.
const deletionRequeueInterval = 30 * time.Second

const (
	configDeletionRetryInterval    = 250 * time.Millisecond
	configDeletionRetryTimeout     = 10 * time.Second
	configWebhookRestoreTimeout    = 10 * time.Second
	configWebhookRestoreAnnotation = "serving.kserve.io/pending-delete-rule-restore"
)

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
// (failurePolicy=Fail). Once no config is referenced, DELETE admission is
// temporarily disabled only for the v1alpha2 config rule while the delete
// requests are accepted, then restored before waiting for finalizers.
func (r *KserveModuleReconciler) cleanupLLMISVCConfigsOnDelete(ctx context.Context, ns string) (configCleanupOutcome, error) {
	// Recover an interrupted pass before any early return, including no configs.
	if err := r.restoreConfigDeletionWebhookDelete(ctx, configDeletionWebhookPatch{}); err != nil {
		return configCleanupOutcome{}, err
	}
	configs, err := r.listWellKnownLLMISVCConfigs(ctx, ns)
	if err != nil {
		return configCleanupOutcome{}, err
	}
	if len(configs) == 0 {
		return configCleanupOutcome{done: true}, nil
	}
	configsToDelete := nonTerminatingConfigs(configs)
	if len(configsToDelete) == 0 {
		return configCleanupOutcome{blockers: []string{"waiting for well-known configs to finish terminating"}}, nil
	}

	// check-before-delete: skip deletion while a config looks referenced. This is
	// a best-effort early guard (the referencedBy read may be slightly stale); the
	// config's own finalizer is the authoritative guard that keeps an in-use config
	// alive even if a delete is issued.
	if blockers := referencedConfigBlockers(configsToDelete); len(blockers) > 0 {
		return configCleanupOutcome{blockers: blockers}, nil
	}

	// Nothing references the configs: temporarily remove DELETE from the
	// dedicated v1alpha2 config webhook rule. DELETE is admitted only when the
	// deletionTimestamp is set; finalizer completion does not require DELETE
	// admission, so restore the rule as soon as the requests have been accepted.
	webhookPatch, err := r.disableConfigDeletionWebhookDelete(ctx)
	if err != nil {
		return configCleanupOutcome{}, err
	}

	deleteErr := r.deleteWellKnownConfigs(ctx, configsToDelete)
	restoreCtx, cancelRestore := context.WithTimeout(context.WithoutCancel(ctx), configWebhookRestoreTimeout)
	restoreErr := r.restoreConfigDeletionWebhookDelete(restoreCtx, webhookPatch)
	cancelRestore()
	if deleteErr != nil || restoreErr != nil {
		return configCleanupOutcome{}, errors.Join(deleteErr, restoreErr)
	}

	// Configs carry a finalizer, so they terminate asynchronously. Block for now;
	// the next reconcile re-lists from the top and reports done once they are gone
	// (or re-blocks if a reference reappeared meanwhile).
	return configCleanupOutcome{blockers: []string{"waiting for well-known configs to finish terminating"}}, nil
}

func nonTerminatingConfigs(configs []unstructured.Unstructured) []unstructured.Unstructured {
	var pending []unstructured.Unstructured
	for i := range configs {
		if configs[i].GetDeletionTimestamp().IsZero() {
			pending = append(pending, configs[i])
		}
	}
	return pending
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
		blocker, err := configDeletionBlocker(&configs[i])
		if err != nil {
			blockers = append(blockers, fmt.Sprintf("%s (status unreadable: %v)", configs[i].GetName(), err))
			continue
		}
		if blocker != "" {
			blockers = append(blockers, fmt.Sprintf("%s (%s)", configs[i].GetName(), blocker))
		}
	}
	sort.Strings(blockers)
	return blockers
}

// configDeletionBlocker reports why cfg cannot safely be deleted. A config is
// eligible only after the llmisvc controller has observed its current generation
// and explicitly marked ConfigInUse=False.
func configDeletionBlocker(cfg *unstructured.Unstructured) (string, error) {
	observedGeneration, found, err := unstructured.NestedInt64(cfg.Object, "status", "observedGeneration")
	if err != nil {
		return "", err
	}
	if !found || observedGeneration != cfg.GetGeneration() {
		return "waiting for llmisvc controller to observe the current generation", nil
	}

	conditions, found, err := unstructured.NestedSlice(cfg.Object, "status", "conditions")
	if err != nil {
		return "", err
	}
	if !found {
		return "waiting for ConfigInUse condition", nil
	}

	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]any)
		if !ok {
			return "", fmt.Errorf("ConfigInUse condition has invalid type %T", condition)
		}
		conditionType, _ := conditionMap["type"].(string)
		if conditionType != "ConfigInUse" {
			continue
		}
		status, ok := conditionMap["status"].(string)
		if !ok {
			return "", fmt.Errorf("ConfigInUse condition has invalid status")
		}
		switch status {
		case "False":
			return "", nil
		case "True":
			refs, err := referencedByNames(cfg)
			if err != nil {
				return "", err
			}
			if len(refs) == 0 {
				return "ConfigInUse=True", nil
			}
			return fmt.Sprintf("referenced by %s", strings.Join(refs, ", ")), nil
		default:
			return fmt.Sprintf("ConfigInUse=%s", status), nil
		}
	}

	return "waiting for ConfigInUse condition", nil
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
		err := wait.PollUntilContextTimeout(ctx, configDeletionRetryInterval, configDeletionRetryTimeout, true, func(ctx context.Context) (bool, error) {
			err := r.Delete(ctx, &configs[i])
			if err == nil || k8serr.IsNotFound(err) {
				return true, nil
			}
			if k8serr.IsForbidden(err) {
				return false, nil
			}
			return false, err
		})
		if err != nil {
			return fmt.Errorf("deleting LLMInferenceServiceConfig %s: %w", configs[i].GetName(), err)
		}
		log.Info("deleted well-known LLMInferenceServiceConfig", "name", configs[i].GetName())
	}
	return nil
}

// configDeletionWebhookPatch records the operations removed from the config
// webhook. It is used to restore exactly those rules after delete admission.
type configDeletionWebhookPatch struct {
	rules []configDeletionWebhookRule
}

type configDeletionWebhookRule struct {
	WebhookName string                                  `json:"webhookName"`
	Rule        admissionregistrationv1.Rule            `json:"rule"`
	Operations  []admissionregistrationv1.OperationType `json:"operations"`
}

func (r *KserveModuleReconciler) configWebhookReader() client.Reader {
	if r.apiReader != nil {
		return r.apiReader
	}
	return r.Client
}

// disableConfigDeletionWebhookDelete removes DELETE only from v1alpha2
// LLMInferenceServiceConfig rules. It returns the original operations needed to
// restore the webhook. An absent webhook is already equivalent to DELETE being
// disabled and therefore needs no restoration.
func (r *KserveModuleReconciler) disableConfigDeletionWebhookDelete(ctx context.Context) (configDeletionWebhookPatch, error) {
	patch := configDeletionWebhookPatch{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		if err := r.configWebhookReader().Get(ctx, client.ObjectKey{Name: llmISVCConfigWebhookName}, webhook); err != nil {
			if k8serr.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("getting %s ValidatingWebhookConfiguration: %w", llmISVCConfigWebhookName, err)
		}

		patch.rules = nil
		if webhook.Annotations[configWebhookRestoreAnnotation] != "" {
			return fmt.Errorf("pending config webhook restoration must complete before disabling DELETE")
		}
		for webhookIndex := range webhook.Webhooks {
			for ruleIndex := range webhook.Webhooks[webhookIndex].Rules {
				rule := &webhook.Webhooks[webhookIndex].Rules[ruleIndex]
				if !isLLMISVCConfigV1alpha2Rule(*rule) || !hasWebhookOperation(rule.Operations, admissionregistrationv1.Delete) {
					continue
				}
				patch.rules = append(patch.rules, configDeletionWebhookRule{
					WebhookName: webhook.Webhooks[webhookIndex].Name,
					Rule:        *rule.Rule.DeepCopy(),
					Operations:  append([]admissionregistrationv1.OperationType(nil), rule.Operations...),
				})
				rule.Operations = withoutWebhookOperation(rule.Operations, admissionregistrationv1.Delete)
			}
		}
		if len(patch.rules) == 0 {
			return fmt.Errorf("no v1alpha2 LLMInferenceServiceConfig DELETE rule found in %s", llmISVCConfigWebhookName)
		}
		saved, err := json.Marshal(patch.rules)
		if err != nil {
			return err
		}
		if webhook.Annotations == nil {
			webhook.Annotations = make(map[string]string)
		}
		// Persist recovery data atomically with disabling admission.
		webhook.Annotations[configWebhookRestoreAnnotation] = string(saved)
		return r.Update(ctx, webhook)
	})
	if err != nil {
		return configDeletionWebhookPatch{}, err
	}
	if len(patch.rules) > 0 {
		ctrl.LoggerFrom(ctx).Info("temporarily disabled config-deletion validating webhook rule", "name", llmISVCConfigWebhookName)
	}
	return patch, nil
}

// restoreConfigDeletionWebhookDelete restores the DELETE operations removed by
// disableConfigDeletionWebhookDelete. It uses a fresh read on every conflict so
// unrelated webhook updates are preserved.
func (r *KserveModuleReconciler) restoreConfigDeletionWebhookDelete(ctx context.Context, patch configDeletionWebhookPatch) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		if err := r.configWebhookReader().Get(ctx, client.ObjectKey{Name: llmISVCConfigWebhookName}, webhook); err != nil {
			if k8serr.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("getting %s ValidatingWebhookConfiguration for restore: %w", llmISVCConfigWebhookName, err)
		}
		saved := webhook.Annotations[configWebhookRestoreAnnotation]
		if saved == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(saved), &patch.rules); err != nil {
			return fmt.Errorf("reading pending config webhook restoration: %w", err)
		}
		for _, savedRule := range patch.rules {
			webhookIndex := webhookIndexByName(webhook.Webhooks, savedRule.WebhookName)
			if webhookIndex < 0 {
				return fmt.Errorf("config webhook %q no longer exists", savedRule.WebhookName)
			}
			found := false
			for i := range webhook.Webhooks[webhookIndex].Rules {
				rule := &webhook.Webhooks[webhookIndex].Rules[i]
				if !reflect.DeepEqual(rule.Rule, savedRule.Rule) {
					continue
				}
				found = true
				// Preserve concurrent operation changes; restore only our removal.
				if !hasWebhookOperation(rule.Operations, admissionregistrationv1.Delete) &&
					!hasWebhookOperation(rule.Operations, admissionregistrationv1.OperationAll) {
					if reflect.DeepEqual(rule.Operations, withoutWebhookOperation(savedRule.Operations, admissionregistrationv1.Delete)) {
						rule.Operations = append([]admissionregistrationv1.OperationType(nil), savedRule.Operations...)
					} else {
						rule.Operations = append(rule.Operations, admissionregistrationv1.Delete)
					}
				}
			}
			if !found {
				return fmt.Errorf("config webhook rule %q no longer exists", savedRule.WebhookName)
			}
		}
		delete(webhook.Annotations, configWebhookRestoreAnnotation)
		return r.Update(ctx, webhook)
	}); err != nil {
		return fmt.Errorf("restoring %s ValidatingWebhookConfiguration: %w", llmISVCConfigWebhookName, err)
	}
	ctrl.LoggerFrom(ctx).Info("restored config-deletion validating webhook rule", "name", llmISVCConfigWebhookName)
	return nil
}

func isLLMISVCConfigV1alpha2Rule(rule admissionregistrationv1.RuleWithOperations) bool {
	return hasString(rule.APIGroups, "serving.kserve.io") &&
		hasString(rule.APIVersions, "v1alpha2") &&
		hasString(rule.Resources, "llminferenceserviceconfigs")
}

func hasString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func hasWebhookOperation(operations []admissionregistrationv1.OperationType, operation admissionregistrationv1.OperationType) bool {
	for _, candidate := range operations {
		if candidate == operation {
			return true
		}
	}
	return false
}

func withoutWebhookOperation(operations []admissionregistrationv1.OperationType, operation admissionregistrationv1.OperationType) []admissionregistrationv1.OperationType {
	filtered := make([]admissionregistrationv1.OperationType, 0, len(operations))
	for _, candidate := range operations {
		if candidate != operation {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func webhookIndexByName(webhooks []admissionregistrationv1.ValidatingWebhook, name string) int {
	for i := range webhooks {
		if webhooks[i].Name == name {
			return i
		}
	}
	return -1
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
