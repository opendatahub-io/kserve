# Copyright 2026 The KServe Authors.
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

"""E2E tests for KServe Transformer TLS scenarios (RHOAIENG-60261).

These tests validate SSL/TLS behavior for InferenceService transformers in
ODH/RHOAI deployments, specifically the TLS infrastructure injected by the
kserve controller (deployment_reconciler_odh.go).
"""

import os
import uuid
from contextlib import contextmanager

import pytest
from kserve import (
    KServeClient,
    V1beta1InferenceService,
    V1beta1InferenceServiceSpec,
    V1beta1PredictorSpec,
    V1beta1SKLearnSpec,
    V1beta1TransformerSpec,
    constants,
)
from kubernetes import client
from kubernetes.client import V1Container, V1ResourceRequirements
from kubernetes.stream import stream as k8s_stream

from ..common.utils import KSERVE_TEST_NAMESPACE, wait_for_resource_deletion

# Constants mirrored from pkg/constants/constants_odh.go
SERVICE_CA_BUNDLE_VOLUME_NAME = "openshift-service-ca-bundle"
SERVICE_CA_BUNDLE_MOUNT_PATH = "/etc/odh/openshift-service-ca-bundle"
SERVICE_CA_BUNDLE_CERT_FILE = "service-ca.crt"
OPENSHIFT_SERVICE_CA_CONFIGMAP = "openshift-service-ca.crt"
AUTH_ANNOTATION = "security.opendatahub.io/enable-auth"
SERVING_CERT_ANNOTATION = "service.beta.openshift.io/serving-cert-secret-name"
OAUTH_PROXY_PORT = 8443
AUTH_PROXY_CONTAINER_NAMES = {"kube-rbac-proxy", "oauth-proxy"}


def create_transformer_tls_isvc(
    service_name: str,
) -> V1beta1InferenceService:
    """Create an InferenceService with transformer and TLS auth enabled.

    Args:
        service_name: Name for the InferenceService

    Returns:
        V1beta1InferenceService configured for raw/Standard deployment with auth
    """
    annotations = {
        "serving.kserve.io/deploymentMode": "Standard",
        AUTH_ANNOTATION: "true",
    }

    predictor = V1beta1PredictorSpec(
        min_replicas=1,
        sklearn=V1beta1SKLearnSpec(
            storage_uri="gs://kfserving-examples/models/sklearn/1.0/model",
            resources=V1ResourceRequirements(
                requests={"cpu": "50m", "memory": "128Mi"},
                limits={"cpu": "100m", "memory": "256Mi"},
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
                    requests={"cpu": "50m", "memory": "128Mi"},
                    limits={"cpu": "100m", "memory": "1Gi"},
                ),
                args=["--model_name", "sklearn-iris"],
            ),
        ],
    )

    return V1beta1InferenceService(
        api_version=constants.KSERVE_V1BETA1,
        kind=constants.KSERVE_KIND_INFERENCESERVICE,
        metadata=client.V1ObjectMeta(
            name=service_name,
            namespace=KSERVE_TEST_NAMESPACE,
            annotations=annotations,
        ),
        spec=V1beta1InferenceServiceSpec(
            predictor=predictor,
            transformer=transformer,
        ),
    )


@contextmanager
def managed_isvc(kserve_client: KServeClient, isvc: V1beta1InferenceService):
    """Context manager for InferenceService lifecycle (create, yield, delete)."""
    service_name = isvc.metadata.name
    kserve_client.create(isvc)
    try:
        kserve_client.wait_isvc_ready(service_name, namespace=KSERVE_TEST_NAMESPACE)
        yield service_name
    finally:
        kserve_client.delete(service_name, KSERVE_TEST_NAMESPACE)
        wait_for_resource_deletion(
            read_func=lambda: kserve_client.api_instance.get_namespaced_custom_object(
                constants.KSERVE_GROUP,
                constants.KSERVE_V1BETA1_VERSION,
                KSERVE_TEST_NAMESPACE,
                constants.KSERVE_PLURAL_INFERENCESERVICE,
                service_name,
            ),
        )


def find_container(deployment, container_name):
    """Find a container by name in a deployment's pod template."""
    for c in deployment.spec.template.spec.containers:
        if c.name == container_name:
            return c
    return None


def get_container_env_map(container):
    """Extract container env vars into a dict {name: value}."""
    if not container or not container.env:
        return {}
    return {env.name: env.value for env in container.env}


@pytest.mark.kserve_on_openshift
@pytest.mark.raw
@pytest.mark.transformer
@pytest.mark.asyncio(scope="session")
async def test_transformer_tls_infrastructure(kserve_client):
    """Test 1: Validate end-to-end SSL infrastructure from controller injection.

    Verifies that the kserve controller correctly injects TLS volumes, env vars,
    and args into the transformer deployment when auth is enabled. Also verifies
    the predictor gets the auth proxy sidecar while the transformer does not.
    """
    suffix = str(uuid.uuid4())[:5]
    service_name = f"xfm-tls-infra-{suffix}"
    isvc = create_transformer_tls_isvc(service_name)

    with managed_isvc(kserve_client, isvc) as svc_name:
        apps_api = client.AppsV1Api()
        core_api = client.CoreV1Api()

        # --- Transformer Deployment checks ---
        transformer_deploy = apps_api.read_namespaced_deployment(
            name=f"{svc_name}-transformer",
            namespace=KSERVE_TEST_NAMESPACE,
        )
        pod_spec = transformer_deploy.spec.template.spec

        # Check CA bundle volume
        volume_names = {v.name for v in pod_spec.volumes}
        assert SERVICE_CA_BUNDLE_VOLUME_NAME in volume_names, (
            f"Expected volume {SERVICE_CA_BUNDLE_VOLUME_NAME} not found"
        )
        ca_volume = next(
            v for v in pod_spec.volumes if v.name == SERVICE_CA_BUNDLE_VOLUME_NAME
        )
        assert ca_volume.config_map.name == OPENSHIFT_SERVICE_CA_CONFIGMAP, (
            f"CA bundle volume should reference {OPENSHIFT_SERVICE_CA_CONFIGMAP}"
        )

        # Check kserve-container
        kserve_container = find_container(transformer_deploy, "kserve-container")
        assert kserve_container is not None, "kserve-container not found in transformer"

        # Volume mount
        mount_names = {vm.name for vm in (kserve_container.volume_mounts or [])}
        assert SERVICE_CA_BUNDLE_VOLUME_NAME in mount_names, (
            f"Expected volume mount {SERVICE_CA_BUNDLE_VOLUME_NAME} not found"
        )
        ca_mount = next(
            vm
            for vm in kserve_container.volume_mounts
            if vm.name == SERVICE_CA_BUNDLE_VOLUME_NAME
        )
        assert ca_mount.mount_path == SERVICE_CA_BUNDLE_MOUNT_PATH, (
            f"Expected mount path {SERVICE_CA_BUNDLE_MOUNT_PATH}, got {ca_mount.mount_path}"
        )
        assert ca_mount.read_only is True, "CA bundle mount should be read-only"

        # Env vars
        env_map = get_container_env_map(kserve_container)
        assert env_map.get("SSL_CERT_DIR") == SERVICE_CA_BUNDLE_MOUNT_PATH, (
            f"SSL_CERT_DIR should be {SERVICE_CA_BUNDLE_MOUNT_PATH}"
        )
        assert (
            env_map.get("REQUESTS_CA_BUNDLE")
            == f"{SERVICE_CA_BUNDLE_MOUNT_PATH}/{SERVICE_CA_BUNDLE_CERT_FILE}"
        ), "REQUESTS_CA_BUNDLE should point to service-ca.crt"
        assert (
            env_map.get("PREDICTOR_HOST")
            == f"{svc_name}-predictor.{KSERVE_TEST_NAMESPACE}.svc"
        ), "PREDICTOR_HOST should point to predictor service"
        assert env_map.get("PREDICTOR_PORT") == str(OAUTH_PROXY_PORT), (
            f"PREDICTOR_PORT should be {OAUTH_PROXY_PORT}"
        )
        assert env_map.get("PREDICTOR_PROTOCOL") == "https", (
            "PREDICTOR_PROTOCOL should be https"
        )

        # --predictor_use_ssl arg
        assert (
            kserve_container.args is not None
            and "--predictor_use_ssl" in kserve_container.args
        ), "--predictor_use_ssl arg missing"
        ssl_idx = kserve_container.args.index("--predictor_use_ssl")
        assert (
            ssl_idx + 1 < len(kserve_container.args)
            and kserve_container.args[ssl_idx + 1] == "true"
        ), "--predictor_use_ssl value should be 'true'"

        # No auth proxy on transformer
        container_names = {c.name for c in pod_spec.containers}
        assert not container_names.intersection(AUTH_PROXY_CONTAINER_NAMES), (
            f"Transformer should not have auth proxy, found: "
            f"{container_names.intersection(AUTH_PROXY_CONTAINER_NAMES)}"
        )

        # --- Predictor Deployment checks ---
        predictor_deploy = apps_api.read_namespaced_deployment(
            name=f"{svc_name}-predictor",
            namespace=KSERVE_TEST_NAMESPACE,
        )
        pred_container_names = {
            c.name for c in predictor_deploy.spec.template.spec.containers
        }
        assert pred_container_names.intersection(AUTH_PROXY_CONTAINER_NAMES), (
            f"Predictor should have auth proxy sidecar, found containers: "
            f"{pred_container_names}"
        )

        # --- Service checks ---
        predictor_svc = core_api.read_namespaced_service(
            name=f"{svc_name}-predictor",
            namespace=KSERVE_TEST_NAMESPACE,
        )
        assert SERVING_CERT_ANNOTATION in predictor_svc.metadata.annotations, (
            f"Predictor service missing {SERVING_CERT_ANNOTATION} annotation"
        )
        # Predictor service should have HTTPS port 8443
        pred_ports = {p.port for p in predictor_svc.spec.ports}
        assert OAUTH_PROXY_PORT in pred_ports, (
            f"Predictor service should have port {OAUTH_PROXY_PORT}"
        )

        transformer_svc = core_api.read_namespaced_service(
            name=f"{svc_name}-transformer",
            namespace=KSERVE_TEST_NAMESPACE,
        )
        assert SERVING_CERT_ANNOTATION in transformer_svc.metadata.annotations, (
            f"Transformer service missing {SERVING_CERT_ANNOTATION} annotation"
        )


@pytest.mark.kserve_on_openshift
@pytest.mark.raw
@pytest.mark.transformer
@pytest.mark.asyncio(scope="session")
async def test_transformer_tls_ca_bundle(kserve_client):
    """Test 3: Validate RHOAI CA-bundle configuration is correctly applied at runtime.

    Verifies that the OpenShift service-ca bundle is correctly mounted and
    accessible inside the transformer pod at runtime.
    """
    suffix = str(uuid.uuid4())[:5]
    service_name = f"xfm-tls-cabundle-{suffix}"
    isvc = create_transformer_tls_isvc(service_name)

    with managed_isvc(kserve_client, isvc) as svc_name:
        core_api = client.CoreV1Api()

        # Verify openshift-service-ca.crt ConfigMap exists in the namespace
        # (injected by OpenShift service-ca operator)
        cm = core_api.read_namespaced_config_map(
            name=OPENSHIFT_SERVICE_CA_CONFIGMAP,
            namespace=KSERVE_TEST_NAMESPACE,
        )
        assert SERVICE_CA_BUNDLE_CERT_FILE in cm.data, (
            f"ConfigMap {OPENSHIFT_SERVICE_CA_CONFIGMAP} missing "
            f"{SERVICE_CA_BUNDLE_CERT_FILE} key"
        )
        assert "BEGIN CERTIFICATE" in cm.data[SERVICE_CA_BUNDLE_CERT_FILE], (
            "CA bundle should contain a PEM certificate"
        )

        # Find the transformer pod
        pods = core_api.list_namespaced_pod(
            namespace=KSERVE_TEST_NAMESPACE,
            label_selector=f"serving.kserve.io/inferenceservice={svc_name},"
            f"component=transformer",
        )
        assert len(pods.items) > 0, "No transformer pod found"
        pod_name = pods.items[0].metadata.name

        # Exec into the transformer pod and verify the cert file is mounted
        exec_command = [
            "cat",
            f"{SERVICE_CA_BUNDLE_MOUNT_PATH}/{SERVICE_CA_BUNDLE_CERT_FILE}",
        ]
        resp = k8s_stream(
            core_api.connect_get_namespaced_pod_exec,
            pod_name,
            KSERVE_TEST_NAMESPACE,
            container="kserve-container",
            command=exec_command,
            stderr=True,
            stdout=True,
            stdin=False,
            tty=False,
        )
        assert "BEGIN CERTIFICATE" in resp, (
            f"Expected PEM certificate at "
            f"{SERVICE_CA_BUNDLE_MOUNT_PATH}/{SERVICE_CA_BUNDLE_CERT_FILE}, "
            f"got: {resp[:100]}"
        )

        # Verify the SSL_CERT_DIR env var is readable from inside the container
        exec_env = ["printenv", "SSL_CERT_DIR"]
        resp_env = k8s_stream(
            core_api.connect_get_namespaced_pod_exec,
            pod_name,
            KSERVE_TEST_NAMESPACE,
            container="kserve-container",
            command=exec_env,
            stderr=True,
            stdout=True,
            stdin=False,
            tty=False,
        )
        assert SERVICE_CA_BUNDLE_MOUNT_PATH in resp_env.strip(), (
            f"SSL_CERT_DIR should be {SERVICE_CA_BUNDLE_MOUNT_PATH}, "
            f"got: {resp_env.strip()}"
        )
