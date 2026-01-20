# Copyright 2022 The KServe Authors.
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

import base64
import json
import os
import uuid
from kubernetes import client
from kubernetes.client import (
    V1ResourceRequirements,
    V1Container,
    V1ContainerPort,
)
from kserve import (
    constants,
    KServeClient,
    V1beta1InferenceService,
    V1beta1InferenceServiceSpec,
    V1beta1PredictorSpec,
    V1beta1SKLearnSpec,
    V1beta1ModelSpec,
    V1beta1ModelFormat,
)
import pytest

from ..common.utils import KSERVE_TEST_NAMESPACE, predict_grpc
from ..common.utils import predict_isvc

api_version = constants.KSERVE_V1BETA1


@pytest.mark.raw
@pytest.mark.asyncio(scope="session")
async def test_raw_deployment_kserve(rest_v1_client, network_layer):
    suffix = str(uuid.uuid4())[1:6]
    service_name = "raw-sklearn-" + suffix
    annotations = dict()
    annotations["serving.kserve.io/deploymentMode"] = "RawDeployment"
    labels = dict()
    labels["networking.kserve.io/visibility"] = "exposed"

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

    isvc = V1beta1InferenceService(
        api_version=constants.KSERVE_V1BETA1,
        kind=constants.KSERVE_KIND_INFERENCESERVICE,
        metadata=client.V1ObjectMeta(
            name=service_name,
            namespace=KSERVE_TEST_NAMESPACE,
            annotations=annotations,
            labels=labels,
        ),
        spec=V1beta1InferenceServiceSpec(predictor=predictor),
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )
    kserve_client.create(isvc)
    kserve_client.wait_isvc_ready_modelstate_loaded(service_name, namespace=KSERVE_TEST_NAMESPACE)
    res = await predict_isvc(
        rest_v1_client,
        service_name,
        "./data/iris_input.json",
        network_layer=network_layer,
    )
    assert res["predictions"] == [1, 1]
    kserve_client.delete(service_name, KSERVE_TEST_NAMESPACE)


@pytest.mark.raw
@pytest.mark.asyncio(scope="session")
async def test_raw_deployment_runtime_kserve(rest_v1_client, network_layer):
    suffix = str(uuid.uuid4())[1:6]
    service_name = "raw-sklearn-runtime-" + suffix
    annotations = dict()
    annotations["serving.kserve.io/deploymentMode"] = "RawDeployment"
    labels = dict()
    labels["networking.kserve.io/visibility"] = "exposed"

    predictor = V1beta1PredictorSpec(
        min_replicas=1,
        model=V1beta1ModelSpec(
            model_format=V1beta1ModelFormat(
                name="sklearn",
            ),
            storage_uri="gs://kfserving-examples/models/sklearn/1.0/model",
            resources=V1ResourceRequirements(
                requests={"cpu": "50m", "memory": "128Mi"},
                limits={"cpu": "100m", "memory": "256Mi"},
            ),
        ),
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
        spec=V1beta1InferenceServiceSpec(predictor=predictor),
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )
    kserve_client.create(isvc)
    kserve_client.wait_isvc_ready_modelstate_loaded(service_name, namespace=KSERVE_TEST_NAMESPACE)
    res = await predict_isvc(
        rest_v1_client,
        service_name,
        "./data/iris_input.json",
        network_layer=network_layer,
    )
    assert res["predictions"] == [1, 1]
    kserve_client.delete(service_name, KSERVE_TEST_NAMESPACE)


@pytest.mark.rawcipn
@pytest.mark.asyncio(scope="session")
async def test_headless_service_address_includes_port():
    """
    Test that status.address.url includes port 8080 when using headless service mode.

    When ServiceClusterIPNone is true (headless mode), the Kubernetes service has
    ClusterIP: None, which means DNS resolves directly to pod IPs without port
    mapping. Users must connect to the container port (8080) directly, not the
    service port (80).

    This test verifies that the InferenceService status.address.url includes
    the :8080 port so that the Dashboard displays the correct internal URL.

    See: RHOAIENG-39715
    """
    suffix = str(uuid.uuid4())[1:6]
    service_name = "raw-headless-port-" + suffix
    annotations = dict()
    annotations["serving.kserve.io/deploymentMode"] = "Standard"
    labels = dict()
    labels["networking.kserve.io/visibility"] = "exposed"

    predictor = V1beta1PredictorSpec(
        min_replicas=1,
        containers=[
            V1Container(
                name="kserve-container",
                image="docker.io/seldonio/mlserver:1.3.2-sklearn",
                ports=[
                    V1ContainerPort(container_port=8080, name="http", protocol="TCP"),
                ],
                args=["mlserver", "start", "/mnt/models"],
                resources=V1ResourceRequirements(
                    requests={"cpu": "50m", "memory": "128Mi"},
                    limits={"cpu": "100m", "memory": "256Mi"},
                ),
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
        spec=V1beta1InferenceServiceSpec(predictor=predictor),
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )
    kserve_client.create(isvc)
    try:
        kserve_client.wait_isvc_ready(service_name, namespace=KSERVE_TEST_NAMESPACE)

        # Get the InferenceService and check the status.address.url
        isvc_status = kserve_client.get(
            service_name,
            namespace=KSERVE_TEST_NAMESPACE,
            version=constants.KSERVE_V1BETA1_VERSION,
        )

        # Verify the service is headless (ClusterIP: None)
        core_api = client.CoreV1Api()
        svc = core_api.read_namespaced_service(
            name=f"{service_name}-predictor",
            namespace=KSERVE_TEST_NAMESPACE,
        )
        assert (
            svc.spec.cluster_ip == "None"
        ), f"Expected headless service (ClusterIP: None), got: {svc.spec.cluster_ip}"

        # Verify that status.address.url includes port 8080
        address_url = isvc_status.get("status", {}).get("address", {}).get("url", "")
        assert ":8080" in address_url, (
            f"Expected status.address.url to include ':8080' for headless service, "
            f"got: {address_url}. "
            f"When using headless mode (ServiceClusterIPNone: true), the internal URL "
            f"must include the container port since there's no service port mapping."
        )
    finally:
        kserve_client.delete(service_name, KSERVE_TEST_NAMESPACE)


@pytest.mark.grpc
@pytest.mark.raw
@pytest.mark.asyncio(scope="session")
@pytest.mark.skip(
    "The custom-model-grpc image fails in OpenShift with a permission denied error"
)
async def test_isvc_with_multiple_container_port(network_layer):
    service_name = "raw-multiport-custom-model"
    model_name = "custom-model"

    predictor = V1beta1PredictorSpec(
        containers=[
            V1Container(
                name="kserve-container",
                image=os.environ.get("CUSTOM_MODEL_GRPC_IMG_TAG"),
                resources=V1ResourceRequirements(
                    requests={"cpu": "50m", "memory": "128Mi"},
                    limits={"cpu": "100m", "memory": "1Gi"},
                ),
                ports=[
                    V1ContainerPort(
                        container_port=8081, name="grpc-port", protocol="TCP"
                    ),
                    V1ContainerPort(
                        container_port=8080, name="http-port", protocol="TCP"
                    ),
                ],
            )
        ]
    )

    isvc = V1beta1InferenceService(
        api_version=constants.KSERVE_V1BETA1,
        kind=constants.KSERVE_KIND_INFERENCESERVICE,
        metadata=client.V1ObjectMeta(
            name=service_name,
            namespace=KSERVE_TEST_NAMESPACE,
            annotations={"serving.kserve.io/deploymentMode": "RawDeployment"},
        ),
        spec=V1beta1InferenceServiceSpec(predictor=predictor),
    )

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config")
    )
    kserve_client.create(isvc)
    kserve_client.wait_isvc_ready_modelstate_loaded(service_name, namespace=KSERVE_TEST_NAMESPACE)

    with open("./data/custom_model_input.json") as json_file:
        data = json.load(json_file)
    payload = [
        {
            "name": "input-0",
            "shape": [],
            "datatype": "BYTES",
            "contents": {
                "bytes_contents": [
                    base64.b64decode(data["instances"][0]["image"]["b64"])
                ]
            },
        }
    ]
    expected_output = ["14.976", "14.037", "13.966", "12.252", "12.086"]
    grpc_response = await predict_grpc(
        service_name=service_name,
        payload=payload,
        model_name=model_name,
        network_layer=network_layer,
    )
    fields = grpc_response.outputs[0].data
    grpc_output = ["%.3f" % value for value in fields]
    assert grpc_output == expected_output
    kserve_client.delete(service_name, KSERVE_TEST_NAMESPACE)
