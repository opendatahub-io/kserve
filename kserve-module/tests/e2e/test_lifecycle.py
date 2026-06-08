"""E2E tests for Kserve CR lifecycle: create, update, delete, CEL validation."""

import json
import yaml
import pytest

from conftest import (
    run,
    _poll_cr,
    wait_for_kserve_cleanup,
    operand_deployments,
    KSERVE_CR_NAME,
    NAMESPACE,
    OPERATOR_DEPLOYMENT,
    TIMEOUT_120S,
)


def _generation_matches(cr):
    gen = cr.get("metadata", {}).get("generation", -1)
    observed = cr.get("status", {}).get("observedGeneration", -2)
    return gen == observed


def _verify_deployments_available(kubectl, is_openshift):
    expected = operand_deployments(is_openshift)
    result = run([kubectl, "get", "deployments", "-n", NAMESPACE, "-o", "yaml"])
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

    def test_delete_cleans_up_managed_resources(self, kubectl, cluster_info, apply_kserve_cr):
        """Kserve CR deletion removes managed deployments but keeps the operator running.

        Verifies GC cleans up operand deployments via ownerReference,
        while the operator deployment itself remains.
        """
        run([kubectl, "delete", "kserve", KSERVE_CR_NAME])
        wait_for_kserve_cleanup(kubectl, is_openshift=cluster_info.is_openshift)

        result = run([kubectl, "get", "kserve", KSERVE_CR_NAME], check=False)
        assert result.returncode != 0, "Kserve CR should be deleted"

        expected = operand_deployments(cluster_info.is_openshift)
        result = run([kubectl, "get", "deployments", "-n", NAMESPACE, "-o", "yaml"])
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

    def test_spec_change_triggers_reconcile(self, kubectl, apply_kserve_cr):
        """Patching spec.rawDeploymentServiceConfig triggers reconcile.

        Verifies generation increments, observedGeneration catches up,
        and the new spec value is persisted.
        """
        gen_before = apply_kserve_cr["metadata"]["generation"]

        result = run([
            kubectl, "get", "configmap", "inferenceservice-config",
            "-n", NAMESPACE, "-o", "jsonpath={.data.service}",
        ])
        assert '"serviceClusterIPNone": true' in result.stdout, \
            "ConfigMap should have serviceClusterIPNone=true before update"

        patch = json.dumps({"spec": {"rawDeploymentServiceConfig": "Headed"}})
        run([kubectl, "patch", "kserve", KSERVE_CR_NAME, "--type", "merge", "-p", patch])

        cr_after = _poll_cr(kubectl, KSERVE_CR_NAME, _generation_matches, TIMEOUT_120S,
                            f"observedGeneration not matching within {TIMEOUT_120S}s")
        gen_after = cr_after["metadata"]["generation"]

        assert gen_after > gen_before, \
            f"Generation should increment: {gen_before} -> {gen_after}"
        assert cr_after["spec"]["rawDeploymentServiceConfig"] == "Headed", \
            "Spec should reflect update: expected Headed"

        expected_headless = "false"
        result = run([
            kubectl, "get", "configmap", "inferenceservice-config",
            "-n", NAMESPACE, "-o", "jsonpath={.data.service}",
        ])
        assert f'"serviceClusterIPNone": {expected_headless}' in result.stdout, \
            f"ConfigMap should reflect serviceClusterIPNone={expected_headless}"


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
            [kubectl, "apply", "-f", "-"], check=False, input_text=invalid_cr
        )

        assert result.returncode != 0, \
            "CR with invalid name should be rejected"
        assert "Kserve name must be 'default-kserve'" in result.stderr, \
            f"Error should reference CEL name validation. stderr: {result.stderr}"

        result = run([kubectl, "get", "kserve", "invalid-name"], check=False)
        assert result.returncode != 0, "Invalid CR should not exist"


class TestManagementState:
    """Verify managementState transitions update sub-component config."""

    @pytest.mark.skip(reason="managementState: Removed not yet implemented")
    def test_removed_updates_subcomponent_config(self, kubectl, apply_kserve_cr):
        """Setting nim.managementState to Removed updates sub-component config.

        Not yet implemented - skipped until managementState: Removed is supported.
        """
        patch = json.dumps({"spec": {"nim": {"managementState": "Removed"}}})
        run([kubectl, "patch", "kserve", KSERVE_CR_NAME, "--type", "merge", "-p", patch])

        cr = _poll_cr(kubectl, KSERVE_CR_NAME, _generation_matches, TIMEOUT_120S,
                      f"observedGeneration not matching within {TIMEOUT_120S}s")

        nim_state = cr.get("spec", {}).get("nim", {}).get("managementState")
        assert nim_state == "Removed", f"Expected Removed, got {nim_state}"
