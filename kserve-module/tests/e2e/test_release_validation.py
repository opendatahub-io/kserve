"""Post-ODH-release validation for kserve-module operator installs.

Validates operand health, release image tags, and lightweight serving smoke
after an ODH release cut (e.g. odh-v3.5). Run via:

    KSERVE_RELEASE_TAG=odh-v3.5 make e2e-kserve-module-post-release
"""

import pytest
import yaml

from conftest import (
    ISVC_SMOKE_NAME,
    ISVC_SMOKE_TIMEOUT,
    LOCALMODEL_AGENT_DEPLOYMENT,
    LOCALMODEL_CONTROLLER_DEPLOYMENT,
    MODEL_CONTROLLER_DEPLOYMENT,
    NAMESPACE,
    RELEASE_IMAGE_OPERANDS_OCP,
    RELEASE_IMAGE_OPERANDS_XKS,
    RELEASE_TEST_NAMESPACE,
    WVA_DEPLOYMENT,
    get_conditions,
    get_deployment_container_image,
    image_matches_release_tag,
    operand_deployments,
    resource_exists,
    run,
    wait_for_deployment,
    wait_for_inference_service_ready,
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
class TestReleaseKserveStatus:
    """Verify the Kserve CR reports healthy status after operator reconcile."""

    def test_kserve_ready_condition(self, kubectl, apply_kserve_cr):
        """Given a managed Kserve CR, KServeReady must be True."""
        conditions = get_conditions(kubectl)
        kserve_ready = conditions.get("KServeReady")
        assert kserve_ready is not None, "KServeReady condition missing"
        assert kserve_ready["status"] == "True", (
            f"KServeReady=False: reason={kserve_ready.get('reason')} "
            f"message={kserve_ready.get('message')}"
        )

    @pytest.mark.ocp_only
    def test_model_controller_ready_condition(self, kubectl, apply_kserve_cr):
        """Given OpenShift operands, ModelControllerReady must be True."""
        conditions = get_conditions(kubectl)
        model_controller_ready = conditions.get("ModelControllerReady")
        assert model_controller_ready is not None, "ModelControllerReady condition missing"
        assert model_controller_ready["status"] == "True"


@pytest.mark.post_release
class TestReleaseOperandHealth:
    """Verify core operands are deployed and stable (no CrashLoop from skew)."""

    def test_operand_deployments_available(self, kubectl, cluster_info, apply_kserve_cr):
        """All expected operand deployments for the platform must be Available."""
        for name in operand_deployments(cluster_info.is_openshift):
            wait_for_deployment(kubectl, name)

    @pytest.mark.ocp_only
    def test_odh_model_controller_not_crashlooping(self, kubectl, apply_kserve_cr):
        """odh-model-controller must stay running (catches image/RBAC skew)."""
        wait_for_deployment(kubectl, MODEL_CONTROLLER_DEPLOYMENT)
        restarts = _pod_restart_count(kubectl, NAMESPACE, MODEL_CONTROLLER_DEPLOYMENT)
        assert restarts >= 0, "failed to read odh-model-controller pod status"
        assert restarts == 0, (
            f"odh-model-controller restarted {restarts} times — check RBAC/image skew"
        )

    @pytest.mark.ocp_only
    def test_wva_deployed_when_managed(self, kubectl, apply_kserve_cr_with_wva):
        """WVA controller must deploy when WVA managementState is Managed."""
        wait_for_deployment(kubectl, WVA_DEPLOYMENT)

    @pytest.mark.ocp_only
    def test_localmodel_operands_with_model_cache(
        self, kubectl, model_cache_enabled, apply_kserve_cr
    ):
        """ModelCache enablement must deploy localmodel controller operands."""
        wait_for_deployment(kubectl, LOCALMODEL_CONTROLLER_DEPLOYMENT)
        wait_for_deployment(kubectl, LOCALMODEL_AGENT_DEPLOYMENT)


@pytest.mark.post_release
class TestReleaseImageTags:
    """Verify operand images use the ODH release tag (not floating odh-stable)."""

    def test_operand_images_use_release_tag(self, kubectl, cluster_info, apply_kserve_cr, expected_release_tag):
        """Operand deployment images must reference KSERVE_RELEASE_TAG."""
        operands = (
            RELEASE_IMAGE_OPERANDS_OCP
            if cluster_info.is_openshift
            else RELEASE_IMAGE_OPERANDS_XKS
        )
        mismatches = []
        for deployment_name in operands:
            if not resource_exists(kubectl, "deployment", deployment_name, NAMESPACE):
                mismatches.append(f"{deployment_name}: deployment not found")
                continue
            image = get_deployment_container_image(kubectl, deployment_name)
            if not image_matches_release_tag(image, expected_release_tag):
                mismatches.append(
                    f"{deployment_name}: image={image} expected tag={expected_release_tag}"
                )
        assert not mismatches, "Release image tag mismatches:\n" + "\n".join(mismatches)


@pytest.mark.post_release
class TestReleaseServingCRDs:
    """Verify serving CRDs are installed by the gathered release manifests."""

    def test_inference_service_crd_exists(self, kubectl, apply_kserve_cr):
        """InferenceService CRD must be registered."""
        assert resource_exists(kubectl, "crd", "inferenceservices.serving.kserve.io")

    def test_llm_inference_service_crd_exists(self, kubectl, apply_kserve_cr):
        """LLMInferenceService CRD must be registered."""
        assert resource_exists(kubectl, "crd", "llminferenceservices.serving.kserve.io")


@pytest.mark.post_release
@pytest.mark.ocp_only
class TestReleaseServingSmoke:
    """Lightweight ISVC / LLMISVC smoke after release install."""

    def test_sklearn_inference_service_ready(
        self, kubectl, apply_kserve_cr, release_test_namespace
    ):
        """Deploy a sklearn InferenceService and wait for Ready=True."""
        if not resource_exists(kubectl, "deployment", "kserve-controller-manager", NAMESPACE):
            pytest.skip("kserve-controller-manager not deployed")

        isvc_yaml = yaml.safe_dump(
            {
                "apiVersion": "serving.kserve.io/v1beta1",
                "kind": "InferenceService",
                "metadata": {
                    "name": ISVC_SMOKE_NAME,
                    "namespace": RELEASE_TEST_NAMESPACE,
                    "labels": {
                        "networking.kserve.io/visibility": "exposed",
                    },
                },
                "spec": {
                    "predictor": {
                        "sklearn": {
                            "storageUri": "gs://kfserving-examples/models/sklearn/1.0/model",
                            "resources": {
                                "requests": {"cpu": "50m", "memory": "128Mi"},
                                "limits": {"cpu": "200m", "memory": "256Mi"},
                            },
                        },
                    },
                },
            }
        )
        run([kubectl, "apply", "-f", "-"], input_text=isvc_yaml)
        try:
            wait_for_inference_service_ready(
                kubectl,
                ISVC_SMOKE_NAME,
                RELEASE_TEST_NAMESPACE,
                timeout=ISVC_SMOKE_TIMEOUT,
            )
        finally:
            run(
                [
                    kubectl,
                    "delete",
                    "inferenceservice",
                    ISVC_SMOKE_NAME,
                    "-n",
                    RELEASE_TEST_NAMESPACE,
                    "--ignore-not-found",
                ],
                check=False,
            )

    def test_llmisvc_cr_accepted_by_controller(self, kubectl, apply_kserve_cr, release_test_namespace):
        """LLMInferenceService CR must be accepted and reach a status (not webhook 500)."""
        if not resource_exists(kubectl, "deployment", "llmisvc-controller-manager", NAMESPACE):
            pytest.skip("llmisvc-controller-manager not deployed")

        llmisvc_yaml = yaml.safe_dump(
            {
                "apiVersion": "serving.kserve.io/v1alpha1",
                "kind": "LLMInferenceService",
                "metadata": {
                    "name": "post-release-llmisvc-smoke",
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

        status_result = run(
            [
                kubectl,
                "get",
                "llminferenceservice",
                "post-release-llmisvc-smoke",
                "-n",
                RELEASE_TEST_NAMESPACE,
                "-o",
                "jsonpath={.status.conditions[*].type}",
            ],
            check=False,
        )
        assert status_result.returncode == 0, (
            f"LLMInferenceService has no status: {status_result.stderr}"
        )
        assert status_result.stdout.strip(), "LLMInferenceService status conditions missing"

        run(
            [
                kubectl,
                "delete",
                "llminferenceservice",
                "post-release-llmisvc-smoke",
                "-n",
                RELEASE_TEST_NAMESPACE,
                "--ignore-not-found",
            ],
            check=False,
        )
