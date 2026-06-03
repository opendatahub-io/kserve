"""Shared fixtures for kserve-module E2E tests."""

import subprocess
import time
from dataclasses import dataclass

import pytest
import yaml


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
KSERVE_CR_NAME = "default-kserve"
NAMESPACE = "opendatahub"
OPERATOR_DEPLOYMENT = "kserve-module-controller-manager"

OPERAND_DEPLOYMENTS_XKS = [
    "llmisvc-controller-manager",
]
OPERAND_DEPLOYMENTS_OCP = [
    "kserve-controller-manager",
    "llmisvc-controller-manager",
    "odh-model-controller",
    "model-serving-api",
]

KSERVE_CR_TEMPLATE = {
    "apiVersion": "components.platform.opendatahub.io/v1alpha1",
    "kind": "Kserve",
    "metadata": {"name": KSERVE_CR_NAME},
    "spec": {"managementState": "Managed"},
}


@dataclass
class ClusterInfo:
    is_openshift: bool
    kubectl: str  # "oc" or "kubectl"


# ---------------------------------------------------------------------------
# Helper functions - pure
# ---------------------------------------------------------------------------
def operand_deployments(is_openshift):
    """Return the expected operand deployments for the detected platform."""
    return OPERAND_DEPLOYMENTS_OCP if is_openshift else OPERAND_DEPLOYMENTS_XKS


def is_cr_ready(cr):
    """Check if a Kserve CR dict has Ready=True."""
    conditions = cr.get("status", {}).get("conditions", [])
    return any(c.get("type") == "Ready" and c.get("status") == "True" for c in conditions)


# ---------------------------------------------------------------------------
# Helper functions - shell / kubectl
# ---------------------------------------------------------------------------
def run(cmd, check=True, timeout=60, input_text=None):
    """Run a command and return the result."""
    result = subprocess.run(
        cmd, capture_output=True, text=True, timeout=timeout, input=input_text
    )
    if check and result.returncode != 0:
        raise RuntimeError(
            f"Command failed: {cmd}\nstdout: {result.stdout}\nstderr: {result.stderr}"
        )
    return result


def get_cr(kubectl_bin, name=KSERVE_CR_NAME, check=True):
    """Fetch the Kserve CR and return parsed YAML. Returns None on failure when check=False."""
    result = run([kubectl_bin, "get", "kserve", name, "-o", "yaml"], check=False)
    if result.returncode != 0:
        if check:
            raise RuntimeError(
                f"Failed to get kserve {name}\nstdout: {result.stdout}\nstderr: {result.stderr}"
            )
        return None
    return yaml.safe_load(result.stdout)


def cr_exists(kubectl_bin, name=KSERVE_CR_NAME):
    """Check if the Kserve CR already exists."""
    return get_cr(kubectl_bin, name, check=False) is not None


def trigger_reconcile(kubectl_bin, name=KSERVE_CR_NAME, trigger_id=None):
    """Trigger reconcile by patching an annotation."""
    trigger_id = trigger_id or f"e2e-{int(time.time())}"
    run([
        kubectl_bin, "annotate", "kserve", name,
        f"test-trigger={trigger_id}", "--overwrite",
    ])


def create_kserve_cr(kubectl_bin, cr_dict=None):
    """Create a Kserve CR if it doesn't already exist."""
    if cr_exists(kubectl_bin):
        return _poll_cr(kubectl_bin, KSERVE_CR_NAME, is_cr_ready, 120,
                        f"Kserve CR {KSERVE_CR_NAME} not ready within 120s")
    cr = yaml.safe_dump(cr_dict or KSERVE_CR_TEMPLATE)
    run([kubectl_bin, "create", "-f", "-"], input_text=cr)
    return _poll_cr(kubectl_bin, KSERVE_CR_NAME, is_cr_ready, 120,
                    f"Kserve CR {KSERVE_CR_NAME} not ready within 120s")


# ---------------------------------------------------------------------------
# Helper functions - polling / wait
# ---------------------------------------------------------------------------
def _poll_cr(kubectl_bin, name, predicate, timeout, msg):
    """Poll the Kserve CR until predicate(cr) returns True."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        cr = get_cr(kubectl_bin, name, check=False)
        if cr is None:
            time.sleep(5)
            continue
        if predicate(cr):
            return cr
        time.sleep(5)
    raise TimeoutError(msg)


def wait_for_kserve_cleanup(kubectl_bin, name=KSERVE_CR_NAME, is_openshift=False, timeout=120):
    """Wait until the Kserve CR is fully deleted."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        if not cr_exists(kubectl_bin, name):
            break
        time.sleep(5)
    else:
        raise TimeoutError(f"Kserve CR {name} not deleted within {timeout}s")
    _wait_for_managed_deployments_gc(kubectl_bin, is_openshift, timeout=60)


def _wait_for_managed_deployments_gc(kubectl_bin, is_openshift, timeout=60):
    """Wait until managed deployments are cleaned up by garbage collection."""
    expected = operand_deployments(is_openshift)
    deadline = time.time() + timeout
    last_error = None
    while time.time() < deadline:
        result = run(
            [kubectl_bin, "get", "deployments", "-n", NAMESPACE, "-o", "yaml"], check=False
        )
        if result.returncode != 0:
            last_error = result.stderr.strip()
            time.sleep(5)
            continue
        deployments = yaml.safe_load(result.stdout)
        dep_names = [d["metadata"]["name"] for d in deployments.get("items", [])]
        if all(op not in dep_names for op in expected):
            return
        time.sleep(5)
    suffix = f" (last kubectl error: {last_error})" if last_error else ""
    raise TimeoutError(f"Managed deployments not cleaned up within {timeout}s{suffix}")


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------
@pytest.fixture(scope="session")
def cluster_info():
    """Detect cluster type by checking for OpenShift API resources."""
    result = subprocess.run(
        ["kubectl", "api-resources", "--api-group=config.openshift.io"],
        capture_output=True, text=True, timeout=10
    )
    is_ocp = result.returncode == 0 and "clusterversions" in result.stdout.lower()
    return ClusterInfo(is_openshift=is_ocp, kubectl="kubectl")


@pytest.fixture(scope="session")
def kubectl(cluster_info):
    """Return the kubectl binary name for the cluster."""
    return cluster_info.kubectl


@pytest.fixture
def ensure_kserve_cr(kubectl):
    """Ensure a Kserve CR exists; create if missing, leave in place after test."""
    return create_kserve_cr(kubectl)


@pytest.fixture
def apply_kserve_cr(kubectl, cluster_info):
    """Create a Kserve CR and delete after test."""
    created = not cr_exists(kubectl)
    cr = create_kserve_cr(kubectl)
    yield cr
    if created:
        run([kubectl, "delete", "kserve", KSERVE_CR_NAME, "--ignore-not-found"], check=False)
        wait_for_kserve_cleanup(kubectl, is_openshift=cluster_info.is_openshift)
