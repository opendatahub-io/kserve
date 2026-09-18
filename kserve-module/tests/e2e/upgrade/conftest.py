"""Fixtures for kserve-module upgrade e2e tests."""

import pytest
import yaml

from upgrade import utils


@pytest.fixture(scope="session")
def ensure_kserve_cr(kubectl):
    """Ensure the platform Kserve CR (`default-kserve`) exists before pre-upgrade.

    This is the components.platform.opendatahub.io/v1alpha1 Kserve resource that
    the module controller reconciles to deploy operands (ISVC/LLMISVC stacks).
    """
    utils.create_kserve_cr(kubectl)


@pytest.fixture(scope="session")
def upgrade_namespace(kubectl):
    """Ensure the dedicated upgrade namespace exists; persists across phases."""
    utils.ensure_namespace(kubectl, utils.UPGRADE_NAMESPACE)


@pytest.fixture(scope="session")
def upgrade_workloads_enabled(cluster_info, kubectl):
    return utils.workloads_supported(kubectl, cluster_info.is_openshift)


@pytest.fixture(scope="session")
def upgrade_baseline(pytestconfig, kubectl):
    """Load baseline ConfigMap during post-upgrade runs."""
    if not utils.is_post_upgrade(pytestconfig):
        return {}
    return utils.load_baseline(kubectl)


@pytest.fixture(scope="session")
def deploy_upgrade_workloads(
    pytestconfig, kubectl, upgrade_namespace, upgrade_workloads_enabled
):
    """Deploy ISVC + LLMISVC before pre-upgrade tests; skip on xks CI."""
    if utils.is_post_upgrade(pytestconfig) or not upgrade_workloads_enabled:
        yield
        return

    utils.cleanup_upgrade_workloads(kubectl)
    utils.apply_mlserver_runtime(kubectl)
    utils.apply_manifest(kubectl, "sklearn-iris-isvc.yaml")
    utils.apply_manifest(kubectl, "llmisvc-opt-125m-cpu.yaml")
    utils.wait_for_isvc_ready(kubectl, name=utils.ISVC_NAME)
    utils.wait_for_llmisvc_ready(kubectl, name=utils.LLMISVC_NAME)
    utils.start_background_probe(kubectl)
    yield


@pytest.fixture(scope="session")
def capture_upgrade_baseline(
    pytestconfig,
    request,
    kubectl,
    cluster_info,
    upgrade_namespace,
    upgrade_workloads_enabled,
    deploy_upgrade_workloads,
):
    """Capture and persist baseline after pre-upgrade validations pass.

    Part A expects existing ISVC/LLMISVC pods to keep the same identity across
    the module-controller roll (no predictor restart). Only the module
    controller deployment may replace its pod.
    """
    yield

    if not utils.is_pre_upgrade(pytestconfig):
        return
    if getattr(request.config, "_pre_upgrade_test_failed", False):
        return

    isvc_hash = None
    llmisvc_workloads_ready_hash = None
    if upgrade_workloads_enabled:
        isvc_hash = utils.run_isvc_inference(kubectl)
        llmisvc_workloads_ready_hash = utils.check_llmisvc_workloads_ready(kubectl)

    baseline = utils.build_baseline(
        kubectl,
        cluster_info.is_openshift,
        isvc_hash=isvc_hash,
        llmisvc_workloads_ready_hash=llmisvc_workloads_ready_hash,
        include_workloads=upgrade_workloads_enabled,
    )
    utils.save_baseline(kubectl, baseline)


@pytest.fixture(scope="class")
def new_isvc_manifest():
    """Load sklearn ISVC manifest and rename for post-upgrade Part B."""
    manifest = yaml.safe_load(utils.manifest_path("sklearn-iris-isvc.yaml").read_text())
    manifest["metadata"]["name"] = utils.NEW_ISVC_NAME
    return manifest


@pytest.fixture(scope="class")
def new_llmisvc_manifest():
    """Load LLMISVC manifest and rename for post-upgrade Part B."""
    manifest = yaml.safe_load(
        utils.manifest_path("llmisvc-opt-125m-cpu.yaml").read_text()
    )
    manifest["metadata"]["name"] = utils.NEW_LLMISVC_NAME
    return manifest


@pytest.fixture(scope="class")
def deploy_new_isvc(
    pytestconfig, kubectl, upgrade_namespace, upgrade_workloads_enabled, new_isvc_manifest
):
    """Create a fresh ISVC during post-upgrade Part B."""
    if not utils.is_post_upgrade(pytestconfig) or not upgrade_workloads_enabled:
        pytest.skip("Post-upgrade workload creation requires OpenShift")

    utils._force_delete(kubectl, "inferenceservice", utils.NEW_ISVC_NAME)
    utils.run(
        [kubectl, "apply", "-n", utils.UPGRADE_NAMESPACE, "-f", "-"],
        input_text=yaml.safe_dump(new_isvc_manifest),
    )
    utils.wait_for_isvc_ready(kubectl, name=utils.NEW_ISVC_NAME)
    return utils.NEW_ISVC_NAME


@pytest.fixture(scope="class")
def deploy_new_llmisvc(
    pytestconfig,
    kubectl,
    upgrade_namespace,
    upgrade_workloads_enabled,
    new_llmisvc_manifest,
):
    """Create a fresh LLMISVC during post-upgrade Part B."""
    if not utils.is_post_upgrade(pytestconfig) or not upgrade_workloads_enabled:
        pytest.skip("Post-upgrade workload creation requires OpenShift")

    utils._force_delete(kubectl, "llminferenceservice", utils.NEW_LLMISVC_NAME)
    utils.run(
        [kubectl, "apply", "-n", utils.UPGRADE_NAMESPACE, "-f", "-"],
        input_text=yaml.safe_dump(new_llmisvc_manifest),
    )
    utils.wait_for_llmisvc_ready(kubectl, name=utils.NEW_LLMISVC_NAME)
    return utils.NEW_LLMISVC_NAME
