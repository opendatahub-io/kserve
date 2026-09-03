"""E2E tests for status conditions.

Deployment unavailability, error isolation, and dependency handling are covered
by integration tests (reconciler_int_test.go, dependency_int_test.go) because
SSA re-applies desired state before the readiness check runs, making it
impossible to simulate in E2E.
"""

import pytest

import json

from conftest import (
    LLMISVC_CONFIG_RESOURCE,
    LLMISVC_DEPLOYMENT,
    MODEL_CONTROLLER_DEPLOYMENT,
    NAMESPACE,
    PLATFORM_VERSION_CM,
    TIMEOUT_120S,
    get_cr,
    get_conditions,
    get_jsonpath,
    operand_deployments,
    run,
    wait_for,
)

# Env var name is specific to the platform-version transition test; kept local
# rather than in conftest until another test needs it.
LLMISVC_CONFIG_PREFIX_ENV = "LLM_INFERENCE_SERVICE_CONFIG_PREFIX"


def _release_version(releases, name):
    """Return the version of the release entry named `name`, or None if absent."""
    for r in releases:
        if r.get("name") == name:
            return r.get("version")
    return None


def _version_prefix(version):
    """Mirror reconciler getVersionPrefix: 2.20.0 -> v2-20-0."""
    return "v" + version.replace(".", "-")


def _set_platform_version(kubectl, version):
    """Patch data.platformVersion on the odh-kserve-config ConfigMap."""
    patch = json.dumps({"data": {"platformVersion": version}})
    run([
        kubectl, "patch", "configmap", PLATFORM_VERSION_CM, "-n", NAMESPACE,
        "--type", "merge", "-p", patch,
    ])


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

        if cluster_info.is_openshift:
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

        Asserts KServe (always) and, where omc is deployed, one runtime it contributes
        (MLServer) as proof its metadata was loaded.
        """
        cr = get_cr(kubectl)
        releases = cr.get("status", {}).get("releases", [])
        names = [r.get("name") for r in releases]

        kserve_version = _release_version(releases, "KServe")
        assert kserve_version is not None, f"expected 'KServe' in releases, got {names}"
        assert kserve_version != "", "KServe version should not be empty"

        # Gate on omc being deployed, not on OpenShift — omc runs on XKS too (PR #1798).
        if MODEL_CONTROLLER_DEPLOYMENT in operand_deployments(cluster_info.is_openshift):
            mlserver_version = _release_version(releases, "MLServer")
            assert mlserver_version is not None, f"expected 'MLServer' in releases, got {names}"
            assert mlserver_version != "", "MLServer version should not be empty"


# Upgrade path under test: baseline A, then bump to B, asserting propagation
# after each step so the transition itself is exercised, not just an end state.
_VERSION_A = "2.19.0"
_VERSION_B = "2.20.0"


def _set_and_assert_propagated(kubectl, version):
    """Patch platformVersion and wait until it reaches the release and the env."""
    expected_env = f"{_version_prefix(version)}-kserve-"

    # The ConfigMap is watched (no generation predicate), so a data change
    # triggers reconcile on its own.
    _set_platform_version(kubectl, version)

    def assert_release_updated():
        releases = get_cr(kubectl).get("status", {}).get("releases", [])
        assert _release_version(releases, "platform") == version, \
            f"platform release version not {version}"
    wait_for(assert_release_updated, timeout=TIMEOUT_120S, interval=5)

    def assert_env_updated():
        # The env is written to every container, and the real container
        # name is not known here, so filter on the env name only.
        out = get_jsonpath(
            kubectl, "deployment", LLMISVC_DEPLOYMENT,
            "{.spec.template.spec.containers[*]"
            f".env[?(@.name=='{LLMISVC_CONFIG_PREFIX_ENV}')].value}}",
            namespace=NAMESPACE,
        )
        vals = out.split()
        assert vals, f"{LLMISVC_CONFIG_PREFIX_ENV} not set on {LLMISVC_DEPLOYMENT}"
        assert all(v == expected_env for v in vals), \
            f"expected all {LLMISVC_CONFIG_PREFIX_ENV}={expected_env}, got {vals}"
    wait_for(assert_env_updated, timeout=TIMEOUT_120S, interval=5)

    def assert_presets_versioned():
        # Well-known presets are renamed to <prefix>-<name> on a version change,
        # so a new prefix means new preset objects appear. (Stale-prefix presets
        # are not pruned, so we only assert the new prefix is present.)
        name_prefix = f"{_version_prefix(version)}-"
        result = run([
            kubectl, "get", LLMISVC_CONFIG_RESOURCE, "-n", NAMESPACE,
            "-o", "jsonpath={.items[*].metadata.name}",
        ])
        names = result.stdout.split()
        assert any(n.startswith(name_prefix) for n in names), \
            f"no {LLMISVC_CONFIG_RESOURCE} with prefix {name_prefix}, got {names}"
    wait_for(assert_presets_versioned, timeout=TIMEOUT_120S, interval=5)


@pytest.mark.sanity
class TestPlatformVersionTransition:
    """A platformVersion change propagates to status.releases and the llmisvc env.

    The orchestrator reads status.releases to detect the module version and pick
    an upgrade mode, so a stuck platform version breaks upgrade orchestration.
    """

    def test_platform_version_change_propagates(self, kubectl, ensure_platform_configmap):
        """A platformVersion upgrade (2.19.0 -> 2.20.0) propagates to the release and env."""
        original = get_jsonpath(
            kubectl, "configmap", PLATFORM_VERSION_CM,
            "{.data.platformVersion}", namespace=NAMESPACE,
        )
        try:
            # Set baseline A, then upgrade to B. A->B is the real transition;
            # step A is a no-op if the cluster already holds A.
            _set_and_assert_propagated(kubectl, _VERSION_A)
            _set_and_assert_propagated(kubectl, _VERSION_B)
        finally:
            # Restore the pre-test value; if there was none, remove the key
            # rather than leaving an empty version behind.
            if original:
                _set_platform_version(kubectl, original)
            else:
                run([
                    kubectl, "patch", "configmap", PLATFORM_VERSION_CM, "-n", NAMESPACE,
                    "--type", "merge", "-p", json.dumps({"data": {"platformVersion": None}}),
                ])
