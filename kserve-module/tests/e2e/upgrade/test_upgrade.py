"""Module image upgrade e2e tests (pre/post phases).

Follows the opendatahub-tests upgrade pattern: pre-upgrade deploys workloads,
captures a baseline ConfigMap, and post-upgrade verifies operand and workload
survival after rolling the kserve-module controller image.
"""

import pytest

from upgrade.utils import (
    ISVC_NAME,
    LLMISVC_NAME,
    NEW_ISVC_NAME,
    NEW_LLMISVC_NAME,
    assert_operand_pods_not_recreated,
    assert_pod_uids_unchanged,
    assert_restart_counts_not_increased,
    capture_isvc_baseline,
    capture_kserve_baseline,
    capture_llmisvc_baseline,
    capture_operand_baselines,
    check_llmisvc_workloads_ready,
    get_cr,
    is_cr_ready,
    operand_deployments,
    operand_pod_identity_deployments,
    run_isvc_inference,
    verify_background_probe,
    verify_module_controller_rolled,
    wait_for_deployment,
    wait_for_upgrade_workloads_settled,
)


@pytest.mark.usefixtures("ensure_kserve_cr", "capture_upgrade_baseline")
class TestPreUpgrade:
    """Deploy workloads, verify serving, capture baseline before module roll."""

    @pytest.mark.pre_upgrade
    def test_kserve_ready(self, kubectl):
        cr = get_cr(kubectl)
        assert is_cr_ready(cr), "Kserve CR must be Ready before module upgrade"

    @pytest.mark.pre_upgrade
    def test_operand_deployments_available(self, kubectl, cluster_info):
        for dep in operand_deployments(cluster_info.is_openshift):
            wait_for_deployment(kubectl, dep)

    @pytest.mark.pre_upgrade
    @pytest.mark.ocp_only
    def test_isvc_inference(self, kubectl, upgrade_namespace, deploy_upgrade_workloads):
        run_isvc_inference(kubectl, namespace=upgrade_namespace, name=ISVC_NAME)

    @pytest.mark.pre_upgrade
    @pytest.mark.ocp_only
    def test_llmisvc_workloads_ready(self, kubectl, upgrade_namespace, deploy_upgrade_workloads):
        check_llmisvc_workloads_ready(kubectl, namespace=upgrade_namespace, name=LLMISVC_NAME)


_upgrade_workloads_settled = False


@pytest.fixture(autouse=True)
def settle_upgrade_workloads(
    pytestconfig, kubectl, upgrade_namespace, upgrade_workloads_enabled
):
    """Let operand reconciles finish before comparing workload pod identity."""
    global _upgrade_workloads_settled
    if _upgrade_workloads_settled:
        return
    if not pytestconfig.getoption("--post-upgrade") or not upgrade_workloads_enabled:
        return
    wait_for_upgrade_workloads_settled(kubectl, namespace=upgrade_namespace)
    _upgrade_workloads_settled = True


class TestPostUpgrade:
    """Verify platform and workloads survived the module image roll."""

    @pytest.mark.post_upgrade
    def test_baseline_configmap_exists(self, upgrade_baseline):
        assert upgrade_baseline, "Baseline ConfigMap must exist from pre-upgrade phase"
        assert "kserve" in upgrade_baseline
        assert "operands" in upgrade_baseline
        assert "module_controller" in upgrade_baseline

    @pytest.mark.post_upgrade
    def test_module_controller_rolled(self, kubectl, upgrade_baseline):
        """Module controller must roll to N+1 image and replace its pod."""
        verify_module_controller_rolled(kubectl, upgrade_baseline)

    @pytest.mark.post_upgrade
    def test_kserve_still_ready(self, kubectl, upgrade_baseline):
        current = capture_kserve_baseline(kubectl)
        assert current["ready"], "Kserve CR must remain Ready after module upgrade"
        assert current["uid"] == upgrade_baseline["kserve"]["uid"], (
            "Kserve CR uid must not change during module image roll"
        )

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_background_probe_no_downtime(self, kubectl, upgrade_namespace):
        """Part A: background probe against ISVC health had no failures during roll."""
        verify_background_probe(kubectl, namespace=upgrade_namespace)

    @pytest.mark.post_upgrade
    def test_operand_controllers_available(self, kubectl, cluster_info):
        """Part A: operand controllers redeployed and reach Available."""
        for dep in operand_deployments(cluster_info.is_openshift):
            wait_for_deployment(kubectl, dep)

    @pytest.mark.post_upgrade
    def test_operand_pods_not_restarted(self, kubectl, cluster_info, upgrade_baseline):
        """KServe/LLMISVC operand controllers must survive; module controller may restart."""
        current = capture_operand_baselines(kubectl, cluster_info.is_openshift)
        for dep in operand_pod_identity_deployments(cluster_info.is_openshift):
            assert dep in upgrade_baseline["operands"], f"No baseline for {dep}"
            assert dep in current, f"Operand deployment {dep} missing post-upgrade"
            baseline = upgrade_baseline["operands"][dep]
            assert_operand_pods_not_recreated(
                baseline["pod_uids"], current[dep]["pod_uids"]
            )
            assert_restart_counts_not_increased(
                baseline["restart_counts"], current[dep]["restart_counts"]
            )

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_isvc_survived(self, kubectl, upgrade_namespace, upgrade_baseline):
        baseline = upgrade_baseline["workloads"][ISVC_NAME]
        current = capture_isvc_baseline(kubectl, name=ISVC_NAME, namespace=upgrade_namespace)
        assert current["uid"] == baseline["uid"]
        assert current["generation"] == baseline["generation"]
        assert_pod_uids_unchanged(
            baseline["pod_uids"],
            current["pod_uids"],
            baseline_names=baseline.get("pod_names"),
            current_names=current.get("pod_names"),
        )
        assert_restart_counts_not_increased(
            baseline["restart_counts"], current["restart_counts"]
        )

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_isvc_inference_after_upgrade(self, kubectl, upgrade_namespace, upgrade_baseline):
        current_hash = run_isvc_inference(
            kubectl, namespace=upgrade_namespace, name=ISVC_NAME
        )
        assert current_hash == upgrade_baseline["workloads"][ISVC_NAME]["inference_hash"]

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_llmisvc_survived(self, kubectl, upgrade_namespace, upgrade_baseline):
        baseline = upgrade_baseline["workloads"][LLMISVC_NAME]
        current = capture_llmisvc_baseline(
            kubectl, name=LLMISVC_NAME, namespace=upgrade_namespace
        )
        assert current["uid"] == baseline["uid"]
        assert current["generation"] == baseline["generation"]
        assert_pod_uids_unchanged(
            baseline["pod_uids"],
            current["pod_uids"],
            baseline_names=baseline.get("pod_names"),
            current_names=current.get("pod_names"),
        )
        assert_restart_counts_not_increased(
            baseline["restart_counts"], current["restart_counts"]
        )

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_llmisvc_workloads_ready_after_upgrade(
        self, kubectl, upgrade_namespace, upgrade_baseline
    ):
        current_hash = check_llmisvc_workloads_ready(
            kubectl, namespace=upgrade_namespace, name=LLMISVC_NAME
        )
        baseline_hash = upgrade_baseline["workloads"][LLMISVC_NAME].get(
            "workloads_ready_hash",
            upgrade_baseline["workloads"][LLMISVC_NAME].get("inference_hash"),
        )
        assert current_hash == baseline_hash


class TestPostUpgradeNewWorkloads:
    """Part B: verify new workloads can be created after module upgrade."""

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_create_new_isvc(self, new_isvc_deployed):
        assert new_isvc_deployed == NEW_ISVC_NAME

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_new_isvc_inference(self, kubectl, upgrade_namespace, new_isvc_deployed):
        run_isvc_inference(
            kubectl, namespace=upgrade_namespace, name=new_isvc_deployed
        )

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_create_new_llmisvc(self, new_llmisvc_deployed):
        assert new_llmisvc_deployed == NEW_LLMISVC_NAME

    @pytest.mark.post_upgrade
    @pytest.mark.ocp_only
    def test_new_llmisvc_workloads_ready(self, kubectl, upgrade_namespace, new_llmisvc_deployed):
        check_llmisvc_workloads_ready(
            kubectl, namespace=upgrade_namespace, name=new_llmisvc_deployed
        )
