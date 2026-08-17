"""E2E tests for status conditions.

Deployment unavailability, error isolation, and dependency handling are covered
by integration tests (reconciler_int_test.go, dependency_int_test.go) because
SSA re-applies desired state before the readiness check runs, making it
impossible to simulate in E2E.
"""

import pytest

from conftest import (
    get_cr,
    get_conditions,
)


def _release_version(releases, name):
    """Return the version of the release entry named `name`, or None if absent."""
    for r in releases:
        if r.get("name") == name:
            return r.get("version")
    return None


@pytest.mark.sanity
class TestStatusConditions:
    """Status condition reporting on a shared CR."""

    def test_happy_path_all_conditions(self, kubectl, cluster_info, apply_kserve_cr):
        """All conditions report correctly after successful reconcile."""
        conditions = get_conditions(kubectl)

        assert conditions["Ready"]["status"] == "True"
        assert conditions["ProvisioningSucceeded"]["status"] == "True"
        assert conditions["ProvisioningSucceeded"]["reason"] == "AllResourcesApplied"
        assert conditions["KServeReady"]["status"] == "True"
        assert conditions["KServeReady"]["reason"] == "AllDeploymentsAvailable"
        assert conditions["DependenciesAvailable"]["status"] == "True"
        assert conditions["Degraded"]["status"] == "False"
        assert conditions["Degraded"]["reason"] == "NoDegradation"

        assert conditions["ModelControllerReady"]["status"] == "True"
        assert conditions["ModelControllerReady"]["reason"] == "AllDeploymentsAvailable"

        cr = get_cr(kubectl)
        assert cr["status"]["phase"] == "Ready"
        assert cr["status"]["observedGeneration"] == cr["metadata"]["generation"]

    def test_releases_include_platform_version(self, kubectl, ensure_platform_configmap):
        """status.releases includes a platform entry from the odh-kserve-config ConfigMap."""
        cr = get_cr(kubectl)
        releases = cr.get("status", {}).get("releases", [])
        release_names = {r["name"] for r in releases}
        assert "platform" in release_names, f"expected 'platform' in releases, got {release_names}"

        platform = next(r for r in releases if r["name"] == "platform")
        assert platform["version"] != "", "platform version should not be empty"

    def test_releases_include_component_versions(
        self, kubectl, cluster_info, apply_kserve_cr
    ):
        """Real component_metadata lands on the CR (envtest only sees the fallback).

        Asserts KServe (always) and, on OpenShift, one runtime omc contributes
        (MLServer) as proof its metadata was loaded. On XKS omc is deployed but
        does not install those runtimes, so they are excluded from status.releases.
        """
        cr = get_cr(kubectl)
        releases = cr.get("status", {}).get("releases", [])
        names = [r.get("name") for r in releases]

        kserve_version = _release_version(releases, "KServe")
        assert kserve_version is not None, f"expected 'KServe' in releases, got {names}"
        assert kserve_version != "", "KServe version should not be empty"

        # omc runtime metadata (MLServer, OVMS, ...) is loaded only on OpenShift.
        # On XKS omc runs but installs no runtimes, so they are dropped from
        # status.releases (see setReleaseStatus).
        mlserver_version = _release_version(releases, "MLServer")
        if cluster_info.is_openshift:
            assert mlserver_version is not None, f"expected 'MLServer' in releases, got {names}"
            assert mlserver_version != "", "MLServer version should not be empty"
        else:
            assert mlserver_version is None, f"MLServer should be excluded on XKS, got {names}"
