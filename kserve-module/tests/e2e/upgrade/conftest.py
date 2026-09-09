"""Fixtures for kserve-module upgrade e2e tests."""

import copy

import pytest
import yaml

from upgrade.utils import (
    ISVC_NAME,
    LLMISVC_NAME,
    NEW_ISVC_NAME,
    NEW_LLMISVC_NAME,
    UPGRADE_NAMESPACE,
    apply_manifest,
    build_baseline,
    ensure_namespace,
    is_post_upgrade,
    is_pre_upgrade,
    load_baseline,
    manifest_path,
    run,
    run_isvc_inference,
    run_llmisvc_inference,
    save_baseline,
    start_background_probe,
    wait_for_isvc_ready,
    wait_for_llmisvc_ready,
    workloads_supported,
)


@pytest.fixture(scope="session")
def upgrade_namespace(kubectl):
    """Dedicated namespace for upgrade workloads; persists across phases."""
    ensure_namespace(kubectl, UPGRADE_NAMESPACE)
    return UPGRADE_NAMESPACE


@pytest.fixture(scope="session")
def upgrade_workloads_enabled(cluster_info, kubectl):
    return workloads_supported(kubectl, cluster_info.is_openshift)


@pytest.fixture(scope="session")
def upgrade_baseline(pytestconfig, kubectl, cluster_info, upgrade_workloads_enabled):
    """Load baseline ConfigMap during post-upgrade runs."""
    if not is_post_upgrade(pytestconfig):
        return {}
    return load_baseline(kubectl, namespace=UPGRADE_NAMESPACE)


@pytest.fixture(scope="session")
def deploy_upgrade_workloads(
    pytestconfig, kubectl, upgrade_namespace, upgrade_workloads_enabled
):
    """Deploy ISVC + LLMISVC before pre-upgrade tests; skip on xks CI."""
    if is_post_upgrade(pytestconfig) or not upgrade_workloads_enabled:
        yield
        return

    apply_manifest(kubectl, "mlserver-runtime.yaml", namespace=upgrade_namespace)
    apply_manifest(kubectl, "sklearn-iris-isvc.yaml", namespace=upgrade_namespace)
    apply_manifest(kubectl, "llmisvc-opt-125m-cpu.yaml", namespace=upgrade_namespace)
    wait_for_isvc_ready(kubectl, name=ISVC_NAME, namespace=upgrade_namespace)
    wait_for_llmisvc_ready(kubectl, name=LLMISVC_NAME, namespace=upgrade_namespace)
    start_background_probe(kubectl, namespace=upgrade_namespace)
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
    """Capture and persist baseline after pre-upgrade validations pass."""
    yield

    if not is_pre_upgrade(pytestconfig):
        return
    if getattr(request.config, "_pre_upgrade_test_failed", False):
        return

    isvc_hash = None
    llmisvc_hash = None
    if upgrade_workloads_enabled:
        isvc_hash = run_isvc_inference(kubectl, namespace=upgrade_namespace)
        llmisvc_hash = run_llmisvc_inference(kubectl, namespace=upgrade_namespace)

    baseline = build_baseline(
        kubectl,
        cluster_info.is_openshift,
        isvc_hash=isvc_hash,
        llmisvc_hash=llmisvc_hash,
        include_workloads=upgrade_workloads_enabled,
    )
    save_baseline(kubectl, baseline, namespace=upgrade_namespace)


@pytest.fixture
def new_isvc_manifest():
    """Clone sklearn ISVC manifest with a post-upgrade name."""
    raw = yaml.safe_load(manifest_path("sklearn-iris-isvc.yaml").read_text())
    manifest = copy.deepcopy(raw)
    manifest["metadata"]["name"] = NEW_ISVC_NAME
    return manifest


@pytest.fixture
def new_llmisvc_manifest():
    """Clone LLMISVC manifest with a post-upgrade name."""
    raw = yaml.safe_load(manifest_path("llmisvc-opt-125m-cpu.yaml").read_text())
    manifest = copy.deepcopy(raw)
    manifest["metadata"]["name"] = NEW_LLMISVC_NAME
    return manifest


@pytest.fixture
def new_isvc_deployed(
    pytestconfig, kubectl, upgrade_namespace, upgrade_workloads_enabled, new_isvc_manifest
):
    """Create a fresh ISVC during post-upgrade Part B."""
    if not is_post_upgrade(pytestconfig) or not upgrade_workloads_enabled:
        pytest.skip("Post-upgrade workload creation requires OpenShift")

    run(
        [kubectl, "apply", "-n", upgrade_namespace, "-f", "-"],
        input_text=yaml.safe_dump(new_isvc_manifest),
    )
    wait_for_isvc_ready(kubectl, name=NEW_ISVC_NAME, namespace=upgrade_namespace)
    return NEW_ISVC_NAME


@pytest.fixture
def new_llmisvc_deployed(
    pytestconfig,
    kubectl,
    upgrade_namespace,
    upgrade_workloads_enabled,
    new_llmisvc_manifest,
):
    """Create a fresh LLMISVC during post-upgrade Part B."""
    if not is_post_upgrade(pytestconfig) or not upgrade_workloads_enabled:
        pytest.skip("Post-upgrade workload creation requires OpenShift")

    run(
        [kubectl, "apply", "-n", upgrade_namespace, "-f", "-"],
        input_text=yaml.safe_dump(new_llmisvc_manifest),
    )
    wait_for_llmisvc_ready(kubectl, name=NEW_LLMISVC_NAME, namespace=upgrade_namespace)
    return NEW_LLMISVC_NAME
