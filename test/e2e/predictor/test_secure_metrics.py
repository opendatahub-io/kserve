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

"""E2E tests for secure metrics on ODH controller deployments.

Validates that controller metrics endpoints serve over HTTPS
with authentication and authorization (SecureServing). ODH
replaces the upstream kube-rbac-proxy sidecar with
controller-runtime's built-in SecureServing, which requires
--metrics-secure and tokenreview/SAR RBAC.
"""

import os
import re
import shutil
import subprocess
import uuid

import pytest

KSERVE_NAMESPACE = os.environ.get("KSERVE_NAMESPACE", "opendatahub")
METRICS_PORT = 8443
_METRICS_RBAC_NAME = "e2e-metrics-reader"
_CURL_IMAGE = "curlimages/curl:8.11.1"
_SA_TOKEN_PATH = "/var/run/secrets/kubernetes.io/serviceaccount/token"

MANAGER_ARGS_JSONPATH = "{.spec.template.spec.containers[?(@.name=='manager')].args}"


def _run(cmd, check=True, timeout=60):
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    if check and result.returncode != 0:
        raise RuntimeError(
            f"Command failed: {cmd}\nstdout: {result.stdout}\nstderr: {result.stderr}"
        )
    return result


def _find_kubectl():
    for name in ("oc", "kubectl"):
        if shutil.which(name):
            return name
    raise RuntimeError("Neither 'oc' nor 'kubectl' found in PATH")


@pytest.fixture(scope="module")
def kubectl():
    return _find_kubectl()


def _get_pod_ip(kubectl, deployment_name):
    result = _run(
        [
            kubectl,
            "get",
            "pods",
            "-n",
            KSERVE_NAMESPACE,
            "-l",
            f"control-plane={deployment_name}",
            "-o",
            "jsonpath={.items[0].status.podIP}",
        ]
    )
    ip = result.stdout.strip()
    if not ip:
        raise RuntimeError(f"No pod IP for {deployment_name}")
    return ip


def _get_container_args(kubectl, resource, name):
    result = _run(
        [
            kubectl,
            "get",
            resource,
            name,
            "-n",
            KSERVE_NAMESPACE,
            "-o",
            f"jsonpath={MANAGER_ARGS_JSONPATH}",
        ],
        check=False,
    )
    return result.stdout.strip()


def _curl_metrics(kubectl, target_ip, use_sa_token=False):
    """Curl metrics endpoint from inside the cluster.

    When use_sa_token is True, the runner pod reads its own mounted
    service-account token at runtime instead of receiving the token
    value as a command argument.
    """
    url = f"https://{target_ip}:{METRICS_PORT}/metrics"
    if use_sa_token:
        curl_cmd = (
            f"TOKEN=$(cat {_SA_TOKEN_PATH}) && "
            'curl -s -o /dev/null -w "%{http_code}" --max-time 5 -k '
            f'-H "Authorization: Bearer $TOKEN" {url}'
        )
    else:
        curl_cmd = f'curl -s -o /dev/null -w "%{{http_code}}" --max-time 5 -k {url}'

    pod_name = f"curl-metrics-{uuid.uuid4().hex[:8]}"
    result = _run(
        [
            kubectl,
            "run",
            pod_name,
            "--rm",
            "-i",
            "--restart=Never",
            "-n",
            KSERVE_NAMESPACE,
            f"--image={_CURL_IMAGE}",
            "--",
            "sh",
            "-c",
            curl_cmd,
        ],
        check=False,
        timeout=30,
    )
    stdout = result.stdout.strip()
    match = re.match(r"([1-5]\d{2})", stdout)
    http_code = int(match.group(1)) if match else 0
    return http_code, result.stdout, result.stderr


def _create_metrics_rbac(kubectl):
    name = f"{_METRICS_RBAC_NAME}-{uuid.uuid4().hex[:8]}"
    _run(
        [
            kubectl,
            "create",
            "clusterrolebinding",
            name,
            "--clusterrole=kserve-metrics-reader-cluster-role",
            f"--serviceaccount={KSERVE_NAMESPACE}:default",
        ],
    )
    return name


def _delete_metrics_rbac(kubectl, name):
    _run(
        [
            kubectl,
            "delete",
            "clusterrolebinding",
            name,
            "--ignore-not-found",
        ],
        check=False,
    )


@pytest.fixture
def metrics_reader_rbac(kubectl):
    name = _create_metrics_rbac(kubectl)
    yield
    _delete_metrics_rbac(kubectl, name)


def _assert_secure_args(args, name):
    assert "--metrics-secure" in args, f"--metrics-secure missing in {name}: {args}"
    assert ":8443" in args, f":8443 missing in {name}: {args}"
    assert "127.0.0.1" not in args, f"metrics bound to localhost in {name}: {args}"


@pytest.mark.kserve_on_openshift
class TestSecureMetrics:
    """Verify controller metrics use SecureServing."""

    def test_kserve_controller_args(self, kubectl):
        name = "kserve-controller-manager"
        args = _get_container_args(kubectl, "deployment", name)
        _assert_secure_args(args, name)

    def test_llmisvc_controller_args(self, kubectl):
        name = "llmisvc-controller-manager"
        args = _get_container_args(kubectl, "deployment", name)
        _assert_secure_args(args, name)

    def test_rejects_unauthenticated(self, kubectl):
        pod_ip = _get_pod_ip(kubectl, "kserve-controller-manager")
        code, stdout, stderr = _curl_metrics(kubectl, pod_ip)
        assert code in (401, 403), (
            f"Expected 401/403, got {code}. stdout={stdout}, stderr={stderr}"
        )

    def test_accepts_authenticated(self, kubectl, metrics_reader_rbac):
        pod_ip = _get_pod_ip(kubectl, "kserve-controller-manager")
        code, stdout, stderr = _curl_metrics(kubectl, pod_ip, use_sa_token=True)
        assert code == 200, (
            f"Expected 200, got {code}. stdout={stdout}, stderr={stderr}"
        )
