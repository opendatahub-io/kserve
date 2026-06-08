"""E2E tests for status conditions and drift correction.

Deployment unavailability, error isolation, and dependency handling are covered
by integration tests (reconciler_int_test.go, dependency_int_test.go) because
SSA re-applies desired state before the readiness check runs, making it
impossible to simulate in E2E.
"""

import time

import pytest

from conftest import (
    run,
    get_cr,
    trigger_reconcile,
    NAMESPACE,
)


def _get_conditions(kubectl):
    """Fetch conditions as a dict keyed by condition type."""
    cr = get_cr(kubectl)
    return {c["type"]: c for c in cr.get("status", {}).get("conditions", [])}


@pytest.mark.sanity
class TestStatusConditions:
    """Status condition reporting on a shared CR."""

    def test_happy_path_all_conditions(self, kubectl, cluster_info, apply_kserve_cr):
        """All conditions report correctly after successful reconcile."""
        conditions = _get_conditions(kubectl)

        assert conditions["Ready"]["status"] == "True"
        assert conditions["ProvisioningSucceeded"]["status"] == "True"
        assert conditions["ProvisioningSucceeded"]["reason"] == "AllResourcesApplied"
        assert conditions["KServeReady"]["status"] == "True"
        assert conditions["KServeReady"]["reason"] == "AllDeploymentsAvailable"
        assert conditions["DependenciesAvailable"]["status"] == "True"
        assert conditions["Degraded"]["status"] == "False"
        assert conditions["Degraded"]["reason"] == "NoDegradation"

        if cluster_info.is_openshift:
            assert conditions["ModelControllerReady"]["status"] == "True"
            assert conditions["ModelControllerReady"]["reason"] == "AllDeploymentsAvailable"

        cr = get_cr(kubectl)
        assert cr["status"]["phase"] == "Ready"
        assert cr["status"]["observedGeneration"] == cr["metadata"]["generation"]


@pytest.mark.sanity
class TestDriftCorrection:
    """SSA drift correction — manual edits reverted on reconcile."""

    def test_configmap_edit_reverted(self, kubectl, apply_kserve_cr):
        """Manual ConfigMap edit is reverted by SSA on next reconcile."""
        cm_name = "inferenceservice-config"

        run(
            f"{kubectl} patch configmap {cm_name} -n {NAMESPACE} "
            f"--type merge "
            f"""-p '{{"data":{{"ingress":"{{\\"ingressClassName\\":\\"TAMPERED\\"}}"}}}}' """
        )

        result = run(
            f"{kubectl} get configmap {cm_name} -n {NAMESPACE} "
            f"-o jsonpath='{{.data.ingress}}'"
        )
        assert "TAMPERED" in result.stdout

        trigger_reconcile(kubectl, trigger_id="drift-001")
        time.sleep(15)

        result = run(
            f"{kubectl} get configmap {cm_name} -n {NAMESPACE} "
            f"-o jsonpath='{{.data.ingress}}'"
        )
        assert "TAMPERED" not in result.stdout

        conditions = _get_conditions(kubectl)
        assert conditions["ProvisioningSucceeded"]["status"] == "True"
