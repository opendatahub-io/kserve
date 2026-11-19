# Copyright 2025 The KServe Authors.
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

"""Exercise retirement admission through the deployed API server and webhooks."""

import json
import os
import uuid

import pytest
from kubernetes import client, config

pytestmark = [
    pytest.mark.llminferenceservice,
    pytest.mark.cluster_cpu,
    pytest.mark.cluster_single_node,
]

REMOVED_MESSAGE = (
    "llm-d Technology Preview has been removed; new LLMInferenceServices and "
    "LLMInferenceServiceConfigs cannot be created"
)


@pytest.fixture(scope="module")
def retirement_api():
    configuration = client.Configuration()
    config.load_kube_config(
        config_file=os.path.expanduser(os.environ.get("KUBECONFIG", "~/.kube/config")),
        client_configuration=configuration,
    )
    configuration.proxy = (
        os.getenv("HTTPS_PROXY") or os.getenv("HTTP_PROXY") or configuration.proxy
    )
    with client.ApiClient(configuration) as api:
        yield api


@pytest.fixture(
    scope="module",
    params=["llmd-retirement-", "llmd-retirement-other-"],
    ids=["first-namespace", "second-namespace"],
)
def retirement_namespace(request, retirement_api):
    """Use isolated namespaces to check that admission is not namespace-specific."""
    core_api = client.CoreV1Api(retirement_api)
    namespace = core_api.create_namespace(
        client.V1Namespace(metadata=client.V1ObjectMeta(generate_name=request.param))
    ).metadata.name
    try:
        yield namespace
    finally:
        core_api.delete_namespace(namespace)


@pytest.mark.parametrize(
    "kind,plural",
    [
        ("LLMInferenceService", "llminferenceservices"),
        ("LLMInferenceServiceConfig", "llminferenceserviceconfigs"),
    ],
)
@pytest.mark.parametrize("dry_run", ["All", None], ids=["dry-run", "create"])
def test_llm_creation_rejected(
    retirement_api, retirement_namespace, kind, plural, dry_run
):
    api = client.CustomObjectsApi(retirement_api)
    name = f"retired-{uuid.uuid4().hex[:12]}"
    body = {
        "apiVersion": "serving.kserve.io/v1alpha1",
        "kind": kind,
        "metadata": {"name": name, "namespace": retirement_namespace},
        "spec": {"model": {"uri": "hf://facebook/opt-125m"}},
    }

    # Valid standalone specs must be denied by the retirement guard, not fail
    # later while resolving missing provisioning-era presets or model images.
    # Namespace teardown also cleans up if a regression unexpectedly admits it.
    with pytest.raises(client.ApiException) as exc:
        api.create_namespaced_custom_object(
            "serving.kserve.io",
            "v1alpha1",
            retirement_namespace,
            plural,
            body,
            dry_run=dry_run,
        )

    assert exc.value.status == 403, str(exc.value)
    assert REMOVED_MESSAGE in json.loads(exc.value.body)["message"]

    # A denial (including dry-run admission) must never persist the object.
    with pytest.raises(client.ApiException) as missing:
        api.get_namespaced_custom_object(
            "serving.kserve.io", "v1alpha1", retirement_namespace, plural, name
        )
    assert missing.value.status == 404, str(missing.value)
