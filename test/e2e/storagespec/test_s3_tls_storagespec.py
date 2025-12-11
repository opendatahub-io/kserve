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

from typing import Optional
import os
import time
from base64 import b64decode, b64encode
from contextlib import contextmanager
from kubernetes import client
from kserve import (
    constants,
    KServeClient,
    V1beta1InferenceService,
    V1beta1InferenceServiceSpec,
    V1beta1PredictorSpec,
    V1beta1SKLearnSpec,
    V1beta1StorageSpec,
)
from kubernetes.client import V1ResourceRequirements
import pytest

from ..common.utils import (
    KSERVE_NAMESPACE,
    KSERVE_TEST_NAMESPACE,
    wait_for_resource_deletion,
)


invalid_cert = """
-----BEGIN CERTIFICATE-----
MIIFLTCCAxWgAwIBAgIUF4tP6T1S5H/Gt8BpjFsbXo7f0SYwDQYJKoZIhvcNAQEL
BQAwJjEVMBMGA1UECgwMRXhhbXBsZSBJbmMuMQ0wCwYDVQQDDARyb290MB4XDTI0
MDIxNjE5MTM0M1oXDTI1MDIxNTE5MTM0M1owJjEVMBMGA1UECgwMRXhhbXBsZSBJ
bmMuMQ0wCwYDVQQDDARyb290MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKC
AgEAnafLggtSuJDwmz6MNaeo2Wmjr6S4xuPYMrCcmclG8Z6qPYHGULTojjy+Du49
xQ+Xf54kFICEndFEsi1/ms/OG7gT6D+yK/2qfHHJFDQiR1wpPGUPB39ICPRmKJZG
u98dVGCULFw+ZKNJa9tQhbFU5GZUW/uHfu9S1CHr8TKjQ3C88+weiCZeP+0bOBNd
ED+IgS7E5amLPhyZZOszN2TcGfIUZbhlshyjpEU3dBt7+X7eUCfCAEzlUnB//dTx
PJI5LODjKAUeruCVzxqmPZVd8dcxoOLrO6GeRiLm9tWAVAuc91tMPlqBrx2gxOWC
seWCc8MdwgneLhg7iaO3lgqCxT7UNJN6Vt0RJ4zHz5ix+9rPzNcVoSvPcFHsECFd
Ia0Kw9BemDW+BElvfdcO4WKeKz5tqJeQJV4VNo5FhifquWHnDDwweZGnyHa+Ma0F
nfDNu6EXz9PMaHwPGYYWUbooRiQ1jvokS+peEu4Co7IuT4N1kix3o17Otiboz9vJ
ZktkMO4Q/8H8Mz9u/22t3/TyKgMYp4ng0JohGXU5jmoxGqd1hL0zkxjeakZXj1cz
TyUzNq0TAYdjAc60DUGyO9zPqyppTMjNCAFJwWW3HDGdOpzmlx3q7G7DtqW38f9Z
/wzQNrRzcrjSAlkoMh815U8KLe+46aQU8qKBNRVCWP+TyhsCAwEAAaNTMFEwHQYD
VR0OBBYEFGx6yRBZRO69d5SLJb0HRbX8kdNgMB8GA1UdIwQYMBaAFGx6yRBZRO69
d5SLJb0HRbX8kdNgMA8GA1UdEwEB/wQFMAMBAf8wDQYJKoZIhvcNAQELBQADggIB
ADzivfSrSJE1lhmqJbJ2ZJaq59nyFu9/rNS9UfHYeiy8eBZEygVDWFIAxb8xmbwP
brhGqCxlAW7Ydw/lwwGUndpP93LN9o93eVnEu7evEr4GflRt3++MCNUXjEbY5THV
7XAU+Rm02lwejUJtk3L9Em0PUFiUp38vbLC0oZKAEOqNgGexPOlUI7+WW2kpEWTj
eOmeEOOW2tKcy2pSId9TX6PtzEBIwuiGZLsD/vSQ1yXs0CZE96xqmRlPoQJ2fyBm
ON/3QYs1o8Mns5tMf/hEWu4p7grHvIAIHHVc8Dyn2XlLiXTSWCgrcYn0HeMIXG+7
yxIda8GBJYO2KZ/eLkg/dE2varrQ8JeapO6ozXS1MFYG4rTPEwSmLxjGyu3XD0sb
jv5LBXm6oDvL8kfJO7uqKcizs2rx5HIjuQ6mEEunVlr9jlFlNzkO0rfoeECrtwuW
jtAxrpGonBuGY4CcmjxpvSwaBDOAbZnZG7g5yRQQTA/lOBvgBfzFm6Xsdm/Vtnya
UCOnFrN0vXLkrQVVrdZxxWhz9FN+SUXQyjsR3D+VpJUVWmw9pfiXi8F/JOpjORhe
TbVunBmL9HUClHgUc2B0NSfNyqXSwo+Gp5Kg4iYIw4hJw2EPwilUFafcM8uVDktK
5kwH30e7WUlkXz+j8p1UIuFM5kKHW/OwPBdLU/1Pl5ts
-----END CERTIFICATE-----
"""
invalid_data_connection = (
    '{"type": "s3","access_key_id":"minio","secret_access_key":"minio123",'
    '"endpoint_url":"https://minio-tls-serving-service.kserve.svc:9000",'
    '"bucket":"mlpipeline","region":"us-south","anonymous":"False"}'
)
ssl_error = "[SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed"


@pytest.fixture(scope="session")
def kserve_client():
    return KServeClient(config_file=os.environ.get("KUBECONFIG", "~/.kube/config"))


def create_isvc_resource(
    name: str,
    storage_key: str = "localTLSMinIOCustom",
) -> V1beta1InferenceService:
    predictor = V1beta1PredictorSpec(
        min_replicas=1,
        sklearn=V1beta1SKLearnSpec(
            storage=V1beta1StorageSpec(
                key=storage_key,
                path="sklearn",
                parameters={"bucket": "example-models"},
            ),
            resources=V1ResourceRequirements(
                requests={"cpu": "50m", "memory": "128Mi"},
                limits={"cpu": "100m", "memory": "256Mi"},
            ),
        ),
    )
    return V1beta1InferenceService(
        api_version=constants.KSERVE_V1BETA1,
        kind=constants.KSERVE_KIND_INFERENCESERVICE,
        metadata=client.V1ObjectMeta(
            name=name,
            namespace=KSERVE_TEST_NAMESPACE,
            labels={
                constants.KSERVE_LABEL_NETWORKING_VISIBILITY: constants.KSERVE_LABEL_NETWORKING_VISIBILITY_EXPOSED,
            },
        ),
        spec=V1beta1InferenceServiceSpec(predictor=predictor),
    )


@contextmanager
def managed_isvc(
    kserve_client: KServeClient,
    isvc: V1beta1InferenceService,
):
    service_name = isvc.metadata.name
    kserve_client.create(isvc)
    yield service_name
    try:  # Cleanup if the context is exited without an error or failure
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
    except Exception:
        # Ignore errors during cleanup (e.g., service already deleted)
        pass


def create_odh_trusted_ca_bundle_configmap(kserve_client: KServeClient, data_key: str):
    # Mimic the RHOAI/ODH operators by creating the odh-trusted-ca-bundle configmap containing the custom cert as a global cert
    configmap_name = "odh-trusted-ca-bundle"
    namespace = KSERVE_TEST_NAMESPACE

    minio_tls_custom_certs = kserve_client.core_api.read_namespaced_secret(
        "minio-tls-custom", KSERVE_NAMESPACE
    ).data
    odh_trusted_ca_configmap = client.V1ConfigMap(
        api_version="v1",
        kind="ConfigMap",
        metadata=client.V1ObjectMeta(name=configmap_name),
        data={
            data_key: b64decode(minio_tls_custom_certs["root.crt"]).decode()
        },
    )
    kserve_client.core_api.create_namespaced_config_map(
        namespace=namespace, body=odh_trusted_ca_configmap
    )

    yield configmap_name

    try:
        kserve_client.core_api.delete_namespaced_config_map(
            name=configmap_name, namespace=namespace
        )
        wait_for_resource_deletion(
            read_func=lambda: kserve_client.core_api.read_namespaced_config_map(
                name=configmap_name, namespace=namespace
            ),
        )
    except Exception:
        pass


@pytest.fixture(scope="function")
def odh_trusted_ca_bundle_configmap_global(kserve_client):
    yield from create_odh_trusted_ca_bundle_configmap(kserve_client, "ca-bundle.crt")


@pytest.fixture(scope="function")
def odh_trusted_ca_bundle_configmap_custom(kserve_client):
    yield from create_odh_trusted_ca_bundle_configmap(kserve_client, "odh-ca-bundle.crt")


@pytest.mark.kserve_on_openshift
def test_s3_tls_global_custom_cert_storagespec_kserve(kserve_client, odh_trusted_ca_bundle_configmap_global):
    # Validate that the model is successfully loaded when the global custom cert is present
    service_name = "isvc-sklearn-s3-tls-global-pass"
    isvc = create_isvc_resource(service_name)
    with managed_isvc(kserve_client, isvc):
        check_model_status(kserve_client, service_name, KSERVE_TEST_NAMESPACE, "UpToDate")

    # Patch the odh-trusted-ca-bundle configmap to replace the global custom cert with an invalid cert
    kserve_client.core_api.patch_namespaced_config_map(
        name="odh-trusted-ca-bundle",
        namespace=KSERVE_TEST_NAMESPACE,
        body={"data": {"ca-bundle.crt": invalid_cert.strip()}},
    )

    # Validate that the model fails to load when the global custom cert is not present
    service_name = "isvc-sklearn-s3-tls-global-fail"
    isvc = create_isvc_resource(service_name)
    with managed_isvc(kserve_client, isvc):
        check_model_status(kserve_client, service_name, KSERVE_TEST_NAMESPACE, "BlockedByFailedLoad", ssl_error)


@pytest.mark.kserve_on_openshift
def test_s3_tls_custom_cert_storagespec_kserve(kserve_client, odh_trusted_ca_bundle_configmap_custom):
    # Validate that the model is successfully loaded when the custom cert is present
    service_name = "isvc-sklearn-s3-tls-custom-pass"
    isvc = create_isvc_resource(service_name)
    with managed_isvc(kserve_client, isvc):
        check_model_status(kserve_client, service_name, KSERVE_TEST_NAMESPACE, "UpToDate")

    # Patch the odh-trusted-ca-bundle configmap to replace the custom cert with an invalid cert
    kserve_client.core_api.patch_namespaced_config_map(
        name="odh-trusted-ca-bundle",
        namespace=KSERVE_TEST_NAMESPACE,
        body={"data": {"odh-ca-bundle.crt": invalid_cert.strip()}},
    )

    # Validate that the model fails to load when the custom cert is not present
    service_name = "isvc-sklearn-s3-tls-custom-fail"
    isvc = create_isvc_resource(service_name)
    with managed_isvc(kserve_client, isvc):
        check_model_status(kserve_client, service_name, KSERVE_TEST_NAMESPACE, "BlockedByFailedLoad", ssl_error)


@pytest.mark.kserve_on_openshift
def test_s3_tls_serving_cert_storagespec_kserve(kserve_client):
    # Validate that the model is successfully loaded using the serving cert
    service_name = "isvc-sklearn-s3-tls-serving-pass"
    isvc = create_isvc_resource(service_name, storage_key="localTLSMinIOServing")
    with managed_isvc(kserve_client, isvc):
        check_model_status(kserve_client, service_name, KSERVE_TEST_NAMESPACE, "UpToDate")

    # Remove the cabundle configmap reference containing the serving certificate from the storage config secret
    storage_config_data = kserve_client.core_api.read_namespaced_secret(
        "storage-config", KSERVE_TEST_NAMESPACE
    ).data
    original_data_connection = storage_config_data["localTLSMinIOServing"]

    kserve_client.core_api.patch_namespaced_secret(
        name="storage-config",
        namespace=KSERVE_TEST_NAMESPACE,
        body={"data": {"localTLSMinIOServing": b64encode(invalid_data_connection.encode()).decode()}},
    )

    # Validate that the model fails to load when the serving cert is not present
    service_name = "isvc-sklearn-s3-tls-serving-fail"
    isvc = create_isvc_resource(service_name, storage_key="localTLSMinIOServing")
    with managed_isvc(kserve_client, isvc):
        check_model_status(kserve_client, service_name, KSERVE_TEST_NAMESPACE, "BlockedByFailedLoad", ssl_error)

    # Restore the storage config secret
    kserve_client.core_api.patch_namespaced_secret(
        name="storage-config",
        namespace=KSERVE_TEST_NAMESPACE,
        body={"data": {"localTLSMinIOServing": original_data_connection}},
    )


def check_model_status(
    kserve_client: KServeClient,
    isvc_name: str,
    isvc_namespace: str,
    expected_status: str,
    expected_failure_message: Optional[str] = None,
    timeout_seconds: int = 660,  # Default progressDeadlineSeconds + 60 seconds
    polling_interval: int = 10,
):
    model_status = None
    for _ in range(round(timeout_seconds / polling_interval)):
        time.sleep(polling_interval)
        isvc = kserve_client.get(
            name=isvc_name,
            namespace=isvc_namespace,
            version=constants.KSERVE_V1BETA1_VERSION,
        )
        model_status = isvc.get("status", {}).get("modelStatus", {})

        failure_message_match = True
        if expected_failure_message is not None:
            failure_message_match = expected_failure_message in model_status.get("lastFailureInfo", {}).get("message", "")

        if (
            model_status.get("transitionStatus") == expected_status
            and failure_message_match
        ):
            return

    actual_status = model_status.get("transitionStatus")
    if expected_failure_message is not None:
        actual_failure_message = (
            model_status.get("lastFailureInfo", {}).get("message", "")
        )
        raise RuntimeError(
            f"Expected inferenceservice {isvc_name} to have model transition status '{expected_status}' "
            f"and last failure info '{expected_failure_message}' after timeout, "
            f"but got model transition status '{actual_status}' "
            f"and last failure info '{actual_failure_message}'"
        )
    raise RuntimeError(
        f"Expected inferenceservice {isvc_name} to have model transition status '{expected_status}' "
        f"after timeout, but got '{actual_status}'"
    )
