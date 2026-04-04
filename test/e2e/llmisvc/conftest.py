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

import os

from kubernetes import client, config
from kubernetes.client.rest import ApiException

# Fixture factory - not called explicitly, but must be imported for pytest to discover it.
from .fixtures import (  # noqa: F401
    KSERVE_TEST_NAMESPACE,
    VLLM_E2E_CHAT_TEMPLATE_CM,
    VLLM_E2E_CHAT_TEMPLATE_JINJA,
    VLLM_E2E_CHAT_TEMPLATE_KEY,
    test_case,
)


# This hook is used to ensure that the test names are unique and to ensure that
# the test names are consistent with the cluster marks.
def pytest_collection_modifyitems(config, items):
    for item in items:
        # only touch parameterized tests
        if not hasattr(item, "callspec"):
            continue

        # if there's no [...] suffix (i.e. not parametrized), skip
        if "[" not in item.nodeid:
            continue
        base, rest = item.nodeid.split("[", 1)
        rest = rest.rstrip("]")

        cluster_marks = [
            m.name for m in item.iter_markers() if m.name.startswith("cluster_")
        ]
        if not cluster_marks:
            continue

        new_id = "-".join(cluster_marks + [rest])
        item._nodeid = f"{base}[{new_id}]"


def pytest_configure(config):
    config.addinivalue_line(
        "markers", "llminferenceservice: mark test as an LLM inference service test"
    )


def pytest_sessionstart(session):
    """Ensure vLLM CPU E2E workloads can pass chat warmup (transformers>=4.44)."""
    if os.environ.get("SKIP_VLLM_E2E_CHAT_TEMPLATE_CM", "").lower() in (
        "1",
        "true",
        "yes",
    ):
        return
    try:
        config.load_kube_config()
    except Exception:
        return

    v1 = client.CoreV1Api()
    try:
        v1.read_namespace(KSERVE_TEST_NAMESPACE)
    except ApiException as e:
        if getattr(e, "status", None) == 404:
            return

    cm = client.V1ConfigMap(
        api_version="v1",
        kind="ConfigMap",
        metadata=client.V1ObjectMeta(
            name=VLLM_E2E_CHAT_TEMPLATE_CM,
            namespace=KSERVE_TEST_NAMESPACE,
        ),
        data={VLLM_E2E_CHAT_TEMPLATE_KEY: VLLM_E2E_CHAT_TEMPLATE_JINJA},
    )
    try:
        v1.create_namespaced_config_map(namespace=KSERVE_TEST_NAMESPACE, body=cm)
    except ApiException as e:
        if getattr(e, "status", None) == 409:
            v1.replace_namespaced_config_map(
                VLLM_E2E_CHAT_TEMPLATE_CM, KSERVE_TEST_NAMESPACE, cm
            )
        else:
            raise
