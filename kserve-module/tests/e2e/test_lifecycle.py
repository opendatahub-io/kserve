"""E2E tests for Kserve CR lifecycle: create, update, delete, CEL validation."""

import yaml
import pytest

from conftest import (
    run,
    get_cr,
    create_kserve_cr,
    is_cr_ready,
    _poll_cr,
    wait_for_kserve_cleanup,
    operand_deployments,
    KSERVE_CR_NAME,
    NAMESPACE,
    OPERATOR_DEPLOYMENT,
)


def _generation_matches(cr):
    gen = cr.get("metadata", {}).get("generation", -1)
    observed = cr.get("status", {}).get("observedGeneration", -2)
    return gen == observed


def _verify_deployments_available(kubectl, is_openshift):
    expected = operand_deployments(is_openshift)
    result = run(f"{kubectl} get deployments -n {NAMESPACE} -o yaml")
    deployments = yaml.safe_load(result.stdout)
    items = {d["metadata"]["name"]: d for d in deployments.get("items", [])}
    for name in expected:
        assert name in items, \
            f"{name} not found. Deployments: {list(items.keys())}"
        conditions = {c["type"]: c for c in items[name].get("status", {}).get("conditions", [])}
        avail = conditions.get("Available", {})
        assert avail.get("status") == "True", \
            f"{name} not Available. Condition: {avail}"


@pytest.mark.sanity
class TestCreate:
    """Verify CR creation triggers operand resource deployment."""

    def test_create_deploys_operands(self, kubectl, cluster_info, apply_kserve_cr):
        """Kserve CR creation deploys managed deployments with Available status.

        apply_kserve_cr fixture creates the CR and waits for Ready=True.
        This test verifies the actual cluster state matches.
        """
        _verify_deployments_available(kubectl, is_openshift=cluster_info.is_openshift)


@pytest.mark.sanity
class TestDelete:
    """Verify CR deletion removes managed resources and preserves CRDs."""

    def test_delete_cleans_up_managed_resources(self, kubectl, cluster_info, ensure_kserve_cr):
        """Kserve CR deletion removes managed deployments but keeps the operator running.

        Verifies GC cleans up operand deployments via ownerReference,
        while the operator deployment itself remains.
        """
        run(f"{kubectl} delete kserve {KSERVE_CR_NAME}")
        wait_for_kserve_cleanup(kubectl, is_openshift=cluster_info.is_openshift)

        result = run(f"{kubectl} get kserve {KSERVE_CR_NAME}", check=False)
        assert result.returncode != 0, "Kserve CR should be deleted"

        expected = operand_deployments(cluster_info.is_openshift)
        result = run(f"{kubectl} get deployments -n {NAMESPACE} -o yaml")
        deployments = yaml.safe_load(result.stdout)
        dep_names = [d["metadata"]["name"] for d in deployments.get("items", [])]

        for operand in expected:
            assert operand not in dep_names, \
                f"{operand} should be deleted. Found: {dep_names}"

        assert OPERATOR_DEPLOYMENT in dep_names, \
            "Operator deployment should still be running"


@pytest.mark.sanity
class TestUpdate:
    """Verify spec changes trigger reconcile and apply new config."""

    def test_spec_change_triggers_reconcile(self, kubectl, ensure_kserve_cr):
        """Patching spec.rawDeploymentServiceConfig triggers reconcile.

        Verifies generation increments, observedGeneration catches up,
        and the new spec value is persisted.
        """
        cr_before = get_cr(kubectl)
        gen_before = cr_before["metadata"]["generation"]
        current = cr_before.get("spec", {}).get("rawDeploymentServiceConfig", "Headless")
        new_value = "Headed" if current == "Headless" else "Headless"

        run(
            f'{kubectl} patch kserve {KSERVE_CR_NAME} --type merge '
            f"""-p '{{"spec":{{"rawDeploymentServiceConfig":"{new_value}"}}}}'"""
        )

        cr_after = _poll_cr(kubectl, KSERVE_CR_NAME, _generation_matches, 120,
                            "observedGeneration not matching within 120s")
        gen_after = cr_after["metadata"]["generation"]

        assert gen_after > gen_before, \
            f"Generation should increment: {gen_before} -> {gen_after}"
        assert cr_after["status"]["observedGeneration"] == gen_after, \
            "observedGeneration should match generation after reconcile"
        assert cr_after["spec"]["rawDeploymentServiceConfig"] == new_value, \
            f"Spec should reflect update: expected {new_value}"


@pytest.mark.sanity
class TestCELValidation:
    """Verify CEL validation rules on the Kserve CRD."""

    def test_rejects_invalid_cr_name(self, kubectl):
        """CRD-level CEL rule enforces singleton name 'default-kserve'.

        Attempts to create a CR with name 'invalid-name' and verifies
        the API server rejects it before it reaches the controller.
        """
        invalid_cr = (
            "apiVersion: components.platform.opendatahub.io/v1alpha1\n"
            "kind: Kserve\n"
            "metadata:\n"
            "  name: invalid-name\n"
            "spec:\n"
            "  managementState: Managed\n"
        )

        result = run(
            f"echo '{invalid_cr}' | {kubectl} apply -f -", check=False
        )

        assert result.returncode != 0, \
            "CR with invalid name should be rejected"
        assert "default-kserve" in result.stderr or "invalid" in result.stderr.lower(), \
            f"Error should reference name validation. stderr: {result.stderr}"

        result = run(f"{kubectl} get kserve invalid-name", check=False)
        assert result.returncode != 0, "Invalid CR should not exist"


class TestManagementState:
    """Verify managementState transitions update sub-component config."""

    @pytest.mark.skip(reason="managementState: Removed not yet implemented")
    def test_removed_updates_subcomponent_config(self, kubectl, apply_kserve_cr):
        """Setting nim.managementState to Removed updates sub-component config.

        Not yet implemented — skipped until managementState: Removed is supported.
        """
        run(
            f"{kubectl} patch kserve {KSERVE_CR_NAME} --type merge "
            f"""-p '{{"spec":{{"nim":{{"managementState":"Removed"}}}}}}'"""
        )

        cr = _poll_cr(kubectl, KSERVE_CR_NAME, _generation_matches, 120,
                      "observedGeneration not matching within 120s")

        nim_state = cr.get("spec", {}).get("nim", {}).get("managementState")
        assert nim_state == "Removed", f"Expected Removed, got {nim_state}"

@pytest.mark.sanity
class TestLifecycleE2E:
    """End-to-end lifecycle tests covering create -> update -> delete."""

    def _run_full_lifecycle(self, kubectl, is_openshift):
        expected = operand_deployments(is_openshift)

        cr = create_kserve_cr(kubectl)
        assert is_cr_ready(cr), "CR should be Ready after creation"
        _verify_deployments_available(kubectl, is_openshift)

        current = cr.get("spec", {}).get("rawDeploymentServiceConfig", "Headless")
        new_value = "Headed" if current == "Headless" else "Headless"
        run(
            f'{kubectl} patch kserve {KSERVE_CR_NAME} --type merge '
            f"""-p '{{"spec":{{"rawDeploymentServiceConfig":"{new_value}"}}}}'"""
        )
        cr = _poll_cr(kubectl, KSERVE_CR_NAME, _generation_matches, 120,
                      "observedGeneration not matching within 120s")
        assert cr["status"]["observedGeneration"] == cr["metadata"]["generation"]
        assert cr["spec"]["rawDeploymentServiceConfig"] == new_value

        run(f"{kubectl} delete kserve {KSERVE_CR_NAME}")
        wait_for_kserve_cleanup(kubectl, is_openshift=is_openshift)

        result = run(f"{kubectl} get deployments -n {NAMESPACE} -o yaml")
        deployments = yaml.safe_load(result.stdout)
        dep_names = [d["metadata"]["name"] for d in deployments.get("items", [])]
        for operand in expected:
            assert operand not in dep_names, f"{operand} should be deleted"

    def test_full_lifecycle(self, kubectl, cluster_info):
        """Full lifecycle in a single test: create -> verify -> update -> delete.

        Runs without fixtures to test the complete flow sequentially,
        ensuring no state leaks between phases.
        """
        self._run_full_lifecycle(kubectl, is_openshift=cluster_info.is_openshift)
