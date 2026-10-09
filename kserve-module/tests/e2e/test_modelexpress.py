"""E2E tests for the ModelExpress component: default Removed, Managed, Managed to Removed."""

import json

import pytest

from conftest import (
    run,
    _poll_cr,
    generation_matches,
    get_cr,
    get_conditions,
    get_jsonpath,
    is_cr_ready,
    operand_deployments,
    resource_exists,
    trigger_reconcile,
    wait_consistently,
    wait_for,
    wait_for_deployment,
    wait_for_deployment_gone,
    KSERVE_CR_NAME,
    MODELEXPRESS_CLUSTERROLES_OCP,
    MODELEXPRESS_CLUSTERROLES_XKS,
    MODELEXPRESS_CRDS,
    MODELEXPRESS_DEPLOYMENT,
    NAMESPACE,
    TIMEOUT_120S,
    TIMEOUT_60S,
    TIMEOUT_300S,
)

CONDITION = "ModelExpressReady"
SERVICE_CA_CONFIGMAP = "openshift-service-ca.crt"
AUTH_DELEGATOR_FINALIZER = "modelexpress.opendatahub.io/auth-delegator"
MXS_NAMESPACE = "km-modelexpress-e2e"
MXS_NAME = "enforced"


def _clusterroles(cluster_info):
    if cluster_info.is_openshift:
        return MODELEXPRESS_CLUSTERROLES_OCP
    return MODELEXPRESS_CLUSTERROLES_XKS


def _set_state(kubectl, state):
    patch = json.dumps({"spec": {"modelExpress": {"managementState": state}}})
    run([kubectl, "patch", "kserve", KSERVE_CR_NAME, "--type", "merge", "-p", patch])
    _poll_cr(kubectl, KSERVE_CR_NAME, generation_matches, TIMEOUT_120S,
             f"observedGeneration not matching within {TIMEOUT_120S}s")


def _enable(kubectl):
    _set_state(kubectl, "Managed")
    wait_for_deployment(kubectl, MODELEXPRESS_DEPLOYMENT, timeout=TIMEOUT_300S)


def _disable(kubectl):
    _set_state(kubectl, "Removed")
    wait_for_deployment_gone(kubectl, MODELEXPRESS_DEPLOYMENT, timeout=TIMEOUT_120S)


def _assert_cr_ready(kubectl):
    cr = get_cr(kubectl)
    assert is_cr_ready(cr), f"Kserve CR should be Ready, conditions: {cr['status'].get('conditions')}"


def _assert_condition_cleared(kubectl):
    assert CONDITION not in get_conditions(kubectl), f"{CONDITION} should be cleared"


def _assert_operands_healthy(kubectl, cluster_info):
    for name in operand_deployments(cluster_info.is_openshift):
        wait_for_deployment(kubectl, name)


@pytest.mark.sanity
@pytest.mark.modelexpress
class TestModelExpressLifecycle:
    """spec.modelExpress.managementState drives the ModelExpress operator install."""

    def test_default_removed_installs_nothing(self, kubectl, cluster_info, apply_kserve_cr):
        _poll_cr(kubectl, KSERVE_CR_NAME, generation_matches, TIMEOUT_120S,
                 f"observedGeneration not matching within {TIMEOUT_120S}s")
        state = get_jsonpath(kubectl, "kserve", KSERVE_CR_NAME,
                             "{.spec.modelExpress.managementState}")
        assert state == "Removed", f"modelExpress should default to Removed, got {state!r}"

        def assert_absent():
            assert not resource_exists(kubectl, "deployment", MODELEXPRESS_DEPLOYMENT,
                                       namespace=NAMESPACE)
            for role in _clusterroles(cluster_info):
                assert not resource_exists(kubectl, "clusterrole", role), \
                    f"clusterrole {role} should not exist while Removed"
            assert CONDITION not in get_conditions(kubectl), \
                f"{CONDITION} should not be reported while Removed"

        trigger_reconcile(kubectl)
        wait_consistently(assert_absent, duration=30, interval=5)

    def test_managed_installs_operator(self, kubectl, cluster_info, apply_kserve_cr):
        try:
            _enable(kubectl)

            for crd in MODELEXPRESS_CRDS:
                assert resource_exists(kubectl, "crd", crd), f"CRD {crd} should be installed"
            for role in _clusterroles(cluster_info):
                assert resource_exists(kubectl, "clusterrole", role), \
                    f"clusterrole {role} should exist while Managed"
                subject_ns = get_jsonpath(kubectl, "clusterrolebinding", role,
                                          "{.subjects[0].namespace}")
                assert subject_ns == NAMESPACE, \
                    f"clusterrolebinding {role} should bind the {NAMESPACE} ServiceAccount, got {subject_ns!r}"

            def assert_condition_true():
                cond = get_conditions(kubectl).get(CONDITION)
                assert cond is not None, f"{CONDITION} should be reported while Managed"
                assert cond["status"] == "True", f"{CONDITION} should be True, got {cond}"

            wait_for(assert_condition_true, timeout=TIMEOUT_120S, interval=5)
            wait_for(lambda: _assert_cr_ready(kubectl), timeout=TIMEOUT_120S, interval=5)
            _assert_operands_healthy(kubectl, cluster_info)
        finally:
            _disable(kubectl)

    def test_managed_to_removed_cleans_up_and_keeps_crds(
        self, kubectl, cluster_info, apply_kserve_cr
    ):
        _enable(kubectl)
        _disable(kubectl)

        def assert_cleaned_up():
            for role in _clusterroles(cluster_info):
                assert not resource_exists(kubectl, "clusterrole", role), \
                    f"clusterrole {role} should be deleted after Removed"
                assert not resource_exists(kubectl, "clusterrolebinding", role), \
                    f"clusterrolebinding {role} should be deleted after Removed"
            assert not resource_exists(kubectl, "serviceaccount", MODELEXPRESS_DEPLOYMENT,
                                       namespace=NAMESPACE)
            assert CONDITION not in get_conditions(kubectl), \
                f"{CONDITION} should be cleared after Removed"

        wait_for(assert_cleaned_up, timeout=TIMEOUT_120S, interval=5)
        for crd in MODELEXPRESS_CRDS:
            assert resource_exists(kubectl, "crd", crd), f"CRD {crd} should survive Removed"
        wait_for(lambda: _assert_cr_ready(kubectl), timeout=TIMEOUT_120S, interval=5)
        _assert_operands_healthy(kubectl, cluster_info)

    def test_removed_waits_for_finalizer_holding_servers(self, kubectl, apply_kserve_cr):
        mxs = {
            "apiVersion": "modelexpress.opendatahub.io/v1alpha1",
            "kind": "ModelExpressServer",
            "metadata": {"name": MXS_NAME, "namespace": MXS_NAMESPACE},
            "spec": {
                "metadataBackend": {"kubernetes": {}},
                "security": {
                    "mode": "enforce",
                    "tokenAudiences": ["modelexpress"],
                    "allowedServiceAccounts": [
                        {"namespace": MXS_NAMESPACE, "serviceAccount": "default"},
                    ],
                },
            },
        }
        try:
            _enable(kubectl)
            run([kubectl, "create", "namespace", MXS_NAMESPACE], check=False)
            run([kubectl, "apply", "-f", "-"], input_text=json.dumps(mxs))

            def assert_finalizer_held():
                finalizers = get_jsonpath(kubectl, "modelexpressserver", MXS_NAME,
                                          "{.metadata.finalizers}", namespace=MXS_NAMESPACE)
                assert AUTH_DELEGATOR_FINALIZER in finalizers, \
                    f"operator should add {AUTH_DELEGATOR_FINALIZER}, got {finalizers!r}"

            wait_for(assert_finalizer_held, timeout=TIMEOUT_120S, interval=5)

            _set_state(kubectl, "Removed")

            def assert_removal_blocked():
                cond = get_conditions(kubectl).get(CONDITION)
                assert cond is not None, f"{CONDITION} should be reported while removal is blocked"
                assert cond["status"] == "False", f"{CONDITION} should be False, got {cond}"
                assert cond["reason"] == "RemovalBlocked", f"unexpected reason: {cond}"
                assert f"{MXS_NAMESPACE}/{MXS_NAME}" in cond["message"], cond["message"]

            wait_for(assert_removal_blocked, timeout=TIMEOUT_120S, interval=5)
            wait_consistently(
                lambda: wait_for_deployment(kubectl, MODELEXPRESS_DEPLOYMENT, timeout=5),
                duration=30, interval=5,
            )

            run([kubectl, "delete", "modelexpressserver", MXS_NAME, "-n", MXS_NAMESPACE,
                 "--wait=true", f"--timeout={TIMEOUT_120S}s"], timeout=TIMEOUT_120S + 10)
            assert not resource_exists(kubectl, "modelexpressserver", MXS_NAME,
                                       namespace=MXS_NAMESPACE), \
                "the operator should have released the ModelExpressServer"

            wait_for_deployment_gone(kubectl, MODELEXPRESS_DEPLOYMENT, timeout=TIMEOUT_120S)
            wait_for(lambda: _assert_condition_cleared(kubectl), timeout=TIMEOUT_120S, interval=5)
        finally:
            run([kubectl, "delete", "modelexpressserver", MXS_NAME, "-n", MXS_NAMESPACE,
                 "--ignore-not-found", f"--timeout={TIMEOUT_60S}s"],
                check=False, timeout=TIMEOUT_60S + 10)
            run([kubectl, "patch", "modelexpressserver", MXS_NAME, "-n", MXS_NAMESPACE,
                 "--type", "merge", "-p", json.dumps({"metadata": {"finalizers": None}})],
                check=False)
            run([kubectl, "delete", "namespace", MXS_NAMESPACE, "--ignore-not-found",
                 f"--timeout={TIMEOUT_120S}s"], check=False, timeout=TIMEOUT_120S + 10)
            _disable(kubectl)

    @pytest.mark.ocp_only
    def test_removed_leaves_platform_service_ca_configmap(self, kubectl, apply_kserve_cr):
        _set_state(kubectl, "Removed")
        uid = get_jsonpath(kubectl, "configmap", SERVICE_CA_CONFIGMAP, "{.metadata.uid}",
                           namespace=NAMESPACE)
        assert uid, f"OpenShift should publish {SERVICE_CA_CONFIGMAP} into {NAMESPACE}"

        def assert_untouched():
            now = get_jsonpath(kubectl, "configmap", SERVICE_CA_CONFIGMAP, "{.metadata.uid}",
                               namespace=NAMESPACE)
            assert now == uid, f"{SERVICE_CA_CONFIGMAP} was replaced: uid {uid} -> {now!r}"

        trigger_reconcile(kubectl)
        wait_consistently(assert_untouched, duration=30, interval=5)
