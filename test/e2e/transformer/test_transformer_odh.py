#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import logging
import os

import httpx
import pytest

from kserve import (
    KServeClient,
    V1beta1InferenceService,
    V1beta1InferenceServiceSpec,
    V1beta1PredictorSpec,
    V1beta1TorchServeSpec,
    V1beta1TransformerSpec,
    constants,
)
from kubernetes import client
from kubernetes.client import V1Container, V1EnvVar, V1ResourceRequirements

from ..common.utils import KSERVE_TEST_NAMESPACE, predict_isvc

logger = logging.getLogger(__name__)


@pytest.mark.transformer
@pytest.mark.auth
@pytest.mark.raw
@pytest.mark.asyncio(scope="session")
async def test_predictor_auth(rest_v1_client, network_layer):
    """Verify kube-rbac-proxy auth enforcement on an InferenceService.

    The ODH model controller injects a kube-rbac-proxy sidecar when the
    ``security.opendatahub.io/enable-auth`` annotation is set to ``"true"``.
    The proxy performs a SubjectAccessReview that requires the caller to have
    ``get`` permission on the specific ``inferenceservices`` resource.

    Checks:
      - Request WITHOUT a bearer token is rejected (401 or 403).
      - Request WITH a valid bearer token succeeds (200).
    """
    service_name = "isvc-predictor-auth"
    sa_name = f"{service_name}-test-sa"

    annotations = {
        "security.opendatahub.io/enable-auth": "true",
        "serving.kserve.io/deploymentMode": "RawDeployment",
    }

    labels = {"networking.kserve.io/visibility": "exposed"}

    predictor = V1beta1PredictorSpec(
        min_replicas=1,
        pytorch=V1beta1TorchServeSpec(
            storage_uri="gs://kfserving-examples/models/torchserve/image_classifier/v1",
            protocol_version="v1",
            resources=V1ResourceRequirements(
                requests={"cpu": "10m", "memory": "128Mi"},
                limits={"cpu": "1", "memory": "1Gi"},
            ),
        ),
    )

    transformer = V1beta1TransformerSpec(
        min_replicas=1,
        containers=[
            V1Container(
                image=os.environ.get("IMAGE_TRANSFORMER_IMG_TAG"),
                name="kserve-container",
                resources=V1ResourceRequirements(
                    requests={"cpu": "10m", "memory": "128Mi"},
                    limits={"cpu": "100m", "memory": "1Gi"},
                ),
                args=["--model_name", "mnist"],
                env=[
                    V1EnvVar(
                        name="STORAGE_URI",
                        value="gs://kfserving-examples/models/torchserve/image_classifier/v1",
                    )
                ],
            )
        ],
    )

    isvc = V1beta1InferenceService(
        api_version=constants.KSERVE_V1BETA1,
        kind=constants.KSERVE_KIND_INFERENCESERVICE,
        metadata=client.V1ObjectMeta(
            name=service_name,
            namespace=KSERVE_TEST_NAMESPACE,
            annotations=annotations,
            labels=labels,
        ),
        spec=V1beta1InferenceServiceSpec(predictor=predictor, transformer=transformer),
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )

    test_failed = False

    try:
        kserve_client.create(isvc)
        kserve_client.wait_isvc_ready(service_name, namespace=KSERVE_TEST_NAMESPACE)

        # Grant auth-delegator to the default SA so kube-rbac-proxy can
        # perform TokenReview / SubjectAccessReview.  In production ODH
        # this is handled by odh-model-controller; in CI we set it up here.
        ensure_auth_delegator(kserve_client, KSERVE_TEST_NAMESPACE)

        # Setup RBAC — simulate what the ODH Dashboard does
        token = create_sa_with_isvc_access(
            kserve_client, sa_name, service_name, KSERVE_TEST_NAMESPACE
        )

        # Pre-check: request without token should be rejected
        logger.info("Testing request WITHOUT token (should fail)")
        try:
            await predict_isvc(
                rest_v1_client,
                service_name,
                "./data/transformer.json",
                model_name="mnist",
                network_layer=network_layer,
            )
            pytest.fail("Expected request without token to be rejected")
        except httpx.HTTPStatusError as auth_err:
            assert auth_err.response.status_code in (401, 403), (
                f"Expected 401 or 403, got {auth_err.response.status_code}"
            )
            logger.info("Request without token rejected as expected: %s", auth_err)

        # Main check: request with valid token should succeed
        logger.info("Testing request WITH valid token (should succeed)")
        res = await predict_isvc(
            rest_v1_client,
            service_name,
            "./data/transformer.json",
            model_name="mnist",
            network_layer=network_layer,
            extra_headers={"Authorization": f"Bearer {token}"},
        )
        assert res["predictions"][0] == 2
        logger.info("Request with valid token succeeded")
        logger.info("Auth enforcement test passed")

    except Exception as e:
        test_failed = True
        logger.error("Failed test for %s: %s", service_name, e)
        _log_pod_status(kserve_client, service_name)
        raise
    finally:
        try:
            cleanup_sa(kserve_client, sa_name, KSERVE_TEST_NAMESPACE)
            cleanup_auth_delegator(KSERVE_TEST_NAMESPACE)
            if not _should_skip_deletion(test_failed):
                kserve_client.delete(service_name, KSERVE_TEST_NAMESPACE)
            elif test_failed:
                logger.info(
                    "Skipping deletion of %s due to test failure "
                    "(SKIP_DELETION_ON_FAILURE=True)",
                    service_name,
                )
        except Exception as e:
            logger.warning("Failed to cleanup %s: %s", service_name, e)
            if not test_failed:
                raise


@pytest.mark.transformer
@pytest.mark.auth
@pytest.mark.raw
def test_transformer_auth_tls_infrastructure():
    """Verify TLS infrastructure injection into the transformer deployment.

    When ``security.opendatahub.io/enable-auth`` is ``"true"`` on an
    InferenceService with a transformer, the controller should inject:
      - ``openshift-service-ca-bundle`` volume (from the service-ca ConfigMap)
      - Volume mount into ``kserve-container`` at ``/etc/odh/openshift-service-ca-bundle``
      - ``SSL_CERT_DIR`` and ``REQUESTS_CA_BUNDLE`` env vars for CA trust
      - ``PREDICTOR_HOST``, ``PREDICTOR_PORT``, ``PREDICTOR_PROTOCOL`` env vars
        for the transformer to discover the predictor's TLS endpoint

    The predictor deployment should NOT receive these env vars.
    """
    service_name = "trans-auth-tls-e2e"

    predictor = V1beta1PredictorSpec(
        min_replicas=1,
        pytorch=V1beta1TorchServeSpec(
            storage_uri="gs://kfserving-examples/models/torchserve/image_classifier/v1",
            protocol_version="v1",
            resources=V1ResourceRequirements(
                requests={"cpu": "10m", "memory": "128Mi"},
                limits={"cpu": "1", "memory": "1Gi"},
            ),
        ),
    )

    transformer = V1beta1TransformerSpec(
        min_replicas=1,
        containers=[
            V1Container(
                image=os.environ["IMAGE_TRANSFORMER_IMG_TAG"],
                name="kserve-container",
                resources=V1ResourceRequirements(
                    requests={"cpu": "10m", "memory": "128Mi"},
                    limits={"cpu": "100m", "memory": "1Gi"},
                ),
                args=["--model_name", "mnist"],
                env=[
                    V1EnvVar(
                        name="STORAGE_URI",
                        value="gs://kfserving-examples/models/torchserve/image_classifier/v1",
                    )
                ],
            )
        ],
    )

    annotations = {
        "serving.kserve.io/deploymentMode": "RawDeployment",
        "security.opendatahub.io/enable-auth": "true",
    }

    isvc = V1beta1InferenceService(
        api_version=constants.KSERVE_V1BETA1,
        kind=constants.KSERVE_KIND_INFERENCESERVICE,
        metadata=client.V1ObjectMeta(
            name=service_name,
            namespace=KSERVE_TEST_NAMESPACE,
            annotations=annotations,
        ),
        spec=V1beta1InferenceServiceSpec(predictor=predictor, transformer=transformer),
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )

    test_failed = False

    try:
        kserve_client.create(isvc)
        kserve_client.wait_isvc_ready(service_name, namespace=KSERVE_TEST_NAMESPACE)

        # Verify transformer deployment has TLS infrastructure
        apps_api = client.AppsV1Api()
        transformer_deployment_name = f"{service_name}-transformer"
        transformer_deployment = apps_api.read_namespaced_deployment(
            name=transformer_deployment_name, namespace=KSERVE_TEST_NAMESPACE
        )

        # Check CA bundle volume
        volume_names = [
            v.name for v in transformer_deployment.spec.template.spec.volumes
        ]
        assert "openshift-service-ca-bundle" in volume_names, (
            f"Transformer deployment should have openshift-service-ca-bundle volume, "
            f"got: {volume_names}"
        )

        ca_volume = next(
            v
            for v in transformer_deployment.spec.template.spec.volumes
            if v.name == "openshift-service-ca-bundle"
        )
        assert ca_volume.config_map is not None
        assert ca_volume.config_map.name == "openshift-service-ca.crt"

        # Check kserve-container has TLS volume mount and env vars
        kserve_container = next(
            c
            for c in transformer_deployment.spec.template.spec.containers
            if c.name == "kserve-container"
        )

        mount_names = [vm.name for vm in kserve_container.volume_mounts]
        assert "openshift-service-ca-bundle" in mount_names, (
            f"kserve-container should have CA bundle volume mount, got: {mount_names}"
        )

        ca_mount = next(
            vm
            for vm in kserve_container.volume_mounts
            if vm.name == "openshift-service-ca-bundle"
        )
        assert ca_mount.mount_path == "/etc/odh/openshift-service-ca-bundle"
        assert ca_mount.read_only is True

        env_map = {env.name: env.value for env in kserve_container.env}
        assert env_map.get("SSL_CERT_DIR") == "/etc/odh/openshift-service-ca-bundle", (
            f"Expected SSL_CERT_DIR=/etc/odh/openshift-service-ca-bundle, "
            f"got: {env_map.get('SSL_CERT_DIR')}"
        )
        assert env_map.get("REQUESTS_CA_BUNDLE") == (
            "/etc/odh/openshift-service-ca-bundle/service-ca.crt"
        ), (
            f"Expected REQUESTS_CA_BUNDLE with cert file, "
            f"got: {env_map.get('REQUESTS_CA_BUNDLE')}"
        )
        expected_predictor_host = (
            f"{service_name}-predictor.{KSERVE_TEST_NAMESPACE}.svc"
        )
        assert env_map.get("PREDICTOR_HOST") == expected_predictor_host, (
            f"Expected PREDICTOR_HOST={expected_predictor_host}, "
            f"got: {env_map.get('PREDICTOR_HOST')}"
        )
        assert env_map.get("PREDICTOR_PORT") == "8443", (
            f"Expected PREDICTOR_PORT=8443, got: {env_map.get('PREDICTOR_PORT')}"
        )
        assert env_map.get("PREDICTOR_PROTOCOL") == "https", (
            f"Expected PREDICTOR_PROTOCOL=https, "
            f"got: {env_map.get('PREDICTOR_PROTOCOL')}"
        )

        # Verify predictor deployment does NOT have TLS env vars
        predictor_deployment_name = f"{service_name}-predictor"
        predictor_deployment = apps_api.read_namespaced_deployment(
            name=predictor_deployment_name, namespace=KSERVE_TEST_NAMESPACE
        )

        predictor_kserve_container = next(
            (
                c
                for c in predictor_deployment.spec.template.spec.containers
                if c.name == "kserve-container"
            ),
            None,
        )
        assert predictor_kserve_container is not None, (
            "Predictor deployment should have a kserve-container"
        )
        assert predictor_kserve_container.env is not None, (
            "Predictor kserve-container should have env vars"
        )
        predictor_env_names = [env.name for env in predictor_kserve_container.env]
        assert "PREDICTOR_HOST" not in predictor_env_names, (
            "Predictor should NOT have PREDICTOR_HOST env var"
        )
        assert "PREDICTOR_PORT" not in predictor_env_names, (
            "Predictor should NOT have PREDICTOR_PORT env var"
        )
        assert "PREDICTOR_PROTOCOL" not in predictor_env_names, (
            "Predictor should NOT have PREDICTOR_PROTOCOL env var"
        )

        logger.info("TLS infrastructure verification passed for transformer deployment")

    except Exception as e:
        test_failed = True
        logger.error("Failed test for %s: %s", service_name, e)
        _log_pod_status(kserve_client, service_name)
        raise
    finally:
        try:
            if not _should_skip_deletion(test_failed):
                kserve_client.delete(service_name, KSERVE_TEST_NAMESPACE)
            elif test_failed:
                logger.info(
                    "Skipping deletion of %s due to test failure "
                    "(SKIP_DELETION_ON_FAILURE=True)",
                    service_name,
                )
        except Exception as e:
            logger.warning("Failed to cleanup %s: %s", service_name, e)
            if not test_failed:
                raise


# ---------------------------------------------------------------------------
# RBAC helpers (adapted from test_llm_auth.py for inferenceservices resource)
# ---------------------------------------------------------------------------
def create_sa_with_isvc_access(kserve_client, sa_name, isvc_name, namespace):
    """Create SA + Role (get on inferenceservices) + RoleBinding, return token.

    The kube-rbac-proxy SAR checks whether the caller can ``get`` the specific
    InferenceService resource in the ``serving.kserve.io`` API group.
    """
    core_api = kserve_client.core_api
    rbac_api = client.RbacAuthorizationV1Api()

    # ServiceAccount
    sa = client.V1ServiceAccount(
        metadata=client.V1ObjectMeta(name=sa_name, namespace=namespace)
    )
    try:
        core_api.create_namespaced_service_account(namespace=namespace, body=sa)
        logger.info("Created ServiceAccount %s", sa_name)
    except client.rest.ApiException as e:
        if e.status == 409:
            logger.info("ServiceAccount %s already exists", sa_name)
        else:
            raise

    # Role – grant ``get`` on the specific InferenceService
    role_name = f"{sa_name}-role"
    role = client.V1Role(
        metadata=client.V1ObjectMeta(name=role_name, namespace=namespace),
        rules=[
            client.V1PolicyRule(
                api_groups=["serving.kserve.io"],
                resources=["inferenceservices"],
                resource_names=[isvc_name],
                verbs=["get"],
            )
        ],
    )
    try:
        rbac_api.create_namespaced_role(namespace=namespace, body=role)
        logger.info("Created Role %s", role_name)
    except client.rest.ApiException as e:
        if e.status == 409:
            rbac_api.replace_namespaced_role(
                name=role_name, namespace=namespace, body=role
            )
            logger.info("Updated Role %s", role_name)
        else:
            raise

    # RoleBinding
    binding_name = f"{sa_name}-binding"
    binding = client.V1RoleBinding(
        metadata=client.V1ObjectMeta(name=binding_name, namespace=namespace),
        role_ref=client.V1RoleRef(
            api_group="rbac.authorization.k8s.io",
            kind="Role",
            name=role_name,
        ),
        subjects=[
            client.RbacV1Subject(
                kind="ServiceAccount",
                name=sa_name,
                namespace=namespace,
            )
        ],
    )
    try:
        rbac_api.create_namespaced_role_binding(namespace=namespace, body=binding)
        logger.info("Created RoleBinding %s", binding_name)
    except client.rest.ApiException as e:
        if e.status == 409:
            rbac_api.replace_namespaced_role_binding(
                name=binding_name, namespace=namespace, body=binding
            )
            logger.info("Updated RoleBinding %s", binding_name)
        else:
            raise

    return get_sa_token(kserve_client, sa_name, namespace)


def get_sa_token(kserve_client, sa_name, namespace):
    """Create a short-lived token via the TokenRequest API."""
    audiences = os.environ.get("TOKEN_AUDIENCES", "https://kubernetes.default.svc").split(",")
    token_request = client.AuthenticationV1TokenRequest(
        spec=client.V1TokenRequestSpec(
            audiences=audiences,
            expiration_seconds=3600,
        )
    )
    token_response = kserve_client.core_api.create_namespaced_service_account_token(
        name=sa_name,
        namespace=namespace,
        body=token_request,
    )
    logger.info("Created token for ServiceAccount %s (audiences=%s)", sa_name, audiences)
    return token_response.status.token


def cleanup_sa(kserve_client, sa_name, namespace):
    """Delete SA, Role, and RoleBinding (best-effort)."""
    core_api = kserve_client.core_api
    rbac_api = client.RbacAuthorizationV1Api()

    for resource_name, delete_fn in [
        (
            f"{sa_name}-binding",
            lambda n: rbac_api.delete_namespaced_role_binding(
                name=n, namespace=namespace
            ),
        ),
        (
            f"{sa_name}-role",
            lambda n: rbac_api.delete_namespaced_role(name=n, namespace=namespace),
        ),
        (
            sa_name,
            lambda n: core_api.delete_namespaced_service_account(
                name=n, namespace=namespace
            ),
        ),
    ]:
        try:
            delete_fn(resource_name)
            logger.info("Deleted %s", resource_name)
        except client.rest.ApiException as e:
            if e.status == 404:
                continue
            logger.warning("Failed to delete %s: %s", resource_name, e)
            raise


_AUTH_DELEGATOR_CRB_NAME = "kserve-e2e-auth-delegator"


def ensure_auth_delegator(kserve_client, namespace):
    """Bind ``system:auth-delegator`` to the ``default`` SA in *namespace*.

    kube-rbac-proxy needs to call the TokenReview and SubjectAccessReview APIs.
    In production ODH this is set up by odh-model-controller; in CI we create
    it ourselves.
    """
    rbac_api = client.RbacAuthorizationV1Api()

    crb = client.V1ClusterRoleBinding(
        metadata=client.V1ObjectMeta(name=_AUTH_DELEGATOR_CRB_NAME),
        role_ref=client.V1RoleRef(
            api_group="rbac.authorization.k8s.io",
            kind="ClusterRole",
            name="system:auth-delegator",
        ),
        subjects=[
            client.RbacV1Subject(
                kind="ServiceAccount",
                name="default",
                namespace=namespace,
            )
        ],
    )
    try:
        rbac_api.create_cluster_role_binding(body=crb)
        logger.info("Created ClusterRoleBinding %s", _AUTH_DELEGATOR_CRB_NAME)
    except client.rest.ApiException as e:
        if e.status == 409:
            rbac_api.replace_cluster_role_binding(
                name=_AUTH_DELEGATOR_CRB_NAME, body=crb
            )
            logger.info("Updated ClusterRoleBinding %s", _AUTH_DELEGATOR_CRB_NAME)
        else:
            raise


def cleanup_auth_delegator(namespace):
    """Delete the auth-delegator ClusterRoleBinding (best-effort)."""
    rbac_api = client.RbacAuthorizationV1Api()
    try:
        rbac_api.delete_cluster_role_binding(name=_AUTH_DELEGATOR_CRB_NAME)
        logger.info("Deleted ClusterRoleBinding %s", _AUTH_DELEGATOR_CRB_NAME)
    except client.rest.ApiException as e:
        if e.status == 404:
            return
        logger.warning(
            "Failed to delete ClusterRoleBinding %s: %s",
            _AUTH_DELEGATOR_CRB_NAME,
            e,
        )


def _log_pod_status(kserve_client, service_name):
    """Log pod status for debugging test failures."""
    try:
        pods = kserve_client.core_api.list_namespaced_pod(
            KSERVE_TEST_NAMESPACE,
            label_selector=f"serving.kserve.io/inferenceservice={service_name}",
        )
        for pod in pods.items:
            logger.info("Pod: %s  Phase: %s", pod.metadata.name, pod.status.phase)
    except client.rest.ApiException as e:
        logger.warning("Failed to list pods for %s: %s", service_name, e)


def _should_skip_deletion(test_failed):
    """Check if resource deletion should be skipped based on env vars."""
    skip_all = os.getenv("SKIP_RESOURCE_DELETION", "False").lower() in (
        "true",
        "1",
        "t",
    )
    skip_on_failure = os.getenv("SKIP_DELETION_ON_FAILURE", "False").lower() in (
        "true",
        "1",
        "t",
    )
    return skip_all or (skip_on_failure and test_failed)
