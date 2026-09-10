"""Post-ODH-release smoke for a fresh kserve-module install on OpenShift.

fresh install → odh-model-controller Running + KServeReady=True
→ create one LLMInferenceService and confirm Ready=True.

    PLATFORM=ocp make e2e-kserve-module-post-release
"""

import pytest
import yaml

from conftest import (
    LLMISVC_SMOKE_NAME,
    LLMISVC_SMOKE_TIMEOUT,
    MODEL_CONTROLLER_DEPLOYMENT,
    NAMESPACE,
    RELEASE_TEST_NAMESPACE,
    get_conditions,
    run,
    wait_for_deployment,
    wait_for_llm_inference_service_ready,
)


def _pod_restart_count(kubectl, namespace, name_prefix):
    """Return total container restart count for pods matching a name prefix."""
    result = run(
        [kubectl, "get", "pods", "-n", namespace, "-o", "yaml"],
        check=False,
    )
    if result.returncode != 0:
        return -1
    pods = yaml.safe_load(result.stdout).get("items", [])
    total = 0
    for pod in pods:
        if not pod["metadata"]["name"].startswith(name_prefix):
            continue
        for container in pod.get("status", {}).get("containerStatuses", []):
            total += container.get("restartCount", 0)
    return total


@pytest.mark.post_release
@pytest.mark.ocp_only
class TestPostReleaseSmoke:
    """Fresh OpenShift install: operands healthy, then one LLMISVC Ready."""

    def test_model_controller_running_and_kserve_ready(self, kubectl, apply_kserve_cr):
        """odh-model-controller must be Running and KServeReady must be True."""
        wait_for_deployment(kubectl, MODEL_CONTROLLER_DEPLOYMENT)
        restarts = _pod_restart_count(kubectl, NAMESPACE, MODEL_CONTROLLER_DEPLOYMENT)
        assert restarts >= 0, "failed to read odh-model-controller pod status"
        assert restarts == 0, (
            f"odh-model-controller restarted {restarts} times — check RBAC/image skew"
        )

        conditions = get_conditions(kubectl)
        kserve_ready = conditions.get("KServeReady")
        assert kserve_ready is not None, "KServeReady condition missing"
        assert kserve_ready["status"] == "True", (
            f"KServeReady=False: reason={kserve_ready.get('reason')} "
            f"message={kserve_ready.get('message')}"
        )

    def test_llm_inference_service_ready(
        self, kubectl, apply_kserve_cr, release_test_namespace
    ):
        """Create one LLMInferenceService and wait for Ready=True."""
        llmisvc_yaml = yaml.safe_dump(
            {
                "apiVersion": "serving.kserve.io/v1alpha1",
                "kind": "LLMInferenceService",
                "metadata": {
                    "name": LLMISVC_SMOKE_NAME,
                    "namespace": RELEASE_TEST_NAMESPACE,
                },
                "spec": {
                    "model": {
                        "name": "tinyllama",
                        "uri": "oci://ghcr.io/kserve/openai/gpt2:0.0.1",
                    },
                },
            }
        )
        result = run([kubectl, "apply", "-f", "-"], input_text=llmisvc_yaml, check=False)
        assert result.returncode == 0, (
            f"LLMInferenceService apply failed (webhook/controller error): {result.stderr}"
        )
        try:
            wait_for_llm_inference_service_ready(
                kubectl,
                LLMISVC_SMOKE_NAME,
                RELEASE_TEST_NAMESPACE,
                timeout=LLMISVC_SMOKE_TIMEOUT,
            )
        finally:
            run(
                [
                    kubectl,
                    "delete",
                    "llminferenceservice",
                    LLMISVC_SMOKE_NAME,
                    "-n",
                    RELEASE_TEST_NAMESPACE,
                    "--ignore-not-found",
                ],
                check=False,
            )
