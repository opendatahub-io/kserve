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
import pytest
import requests
from kserve import KServeClient
from kubernetes import client

from .fixtures import (
    KSERVE_TEST_NAMESPACE,
    LLMINFERENCESERVICE_CONFIGS,
    generate_test_id,
    inject_k8s_proxy,
    test_case,  # noqa: F401,F811
)
from .test_llm_inference_service import (
    TestCase,
    create_llmisvc,
    delete_llmisvc,
    get_llmisvc,
    wait_for_llm_isvc_ready,
    get_llm_service_url,
    _collect_diagnostics,
)
from .logging import log_execution, logger


# Add auth-specific configurations
LLMINFERENCESERVICE_CONFIGS["router-auth-disabled"] = {
    "router": {"scheduler": {}, "route": {}, "gateway": {}},
}


def create_service_account_with_get_access(
    kserve_client: KServeClient,
    sa_name: str,
    llm_service_name: str,
    namespace: str = KSERVE_TEST_NAMESPACE,
):
    """
    Create a ServiceAccount with GET access to a specific LLMInferenceService.
    Returns the ServiceAccount token.
    """
    core_api = kserve_client.core_api
    rbac_api = client.RbacAuthorizationV1Api()

    # Create ServiceAccount
    sa = client.V1ServiceAccount(
        metadata=client.V1ObjectMeta(name=sa_name, namespace=namespace)
    )
    try:
        core_api.create_namespaced_service_account(namespace=namespace, body=sa)
        logger.info(f"✅ Created ServiceAccount {sa_name}")
    except client.rest.ApiException as e:
        if e.status == 409:  # Already exists
            logger.info(f"ServiceAccount {sa_name} already exists")
        else:
            raise

    # Create Role with GET permission on the specific LLMInferenceService
    role_name = f"{sa_name}-role"
    role = client.V1Role(
        metadata=client.V1ObjectMeta(name=role_name, namespace=namespace),
        rules=[
            client.V1PolicyRule(
                api_groups=["serving.kserve.io"],
                resources=["llminferenceservices"],
                resource_names=[llm_service_name],
                verbs=["get"],
            )
        ],
    )
    try:
        rbac_api.create_namespaced_role(namespace=namespace, body=role)
        logger.info(f"✅ Created Role {role_name}")
    except client.rest.ApiException as e:
        if e.status == 409:
            rbac_api.replace_namespaced_role(
                name=role_name, namespace=namespace, body=role
            )
            logger.info(f"✅ Updated Role {role_name}")
        else:
            raise

    # Create RoleBinding
    role_binding_name = f"{sa_name}-binding"
    role_binding = client.V1RoleBinding(
        metadata=client.V1ObjectMeta(name=role_binding_name, namespace=namespace),
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
        rbac_api.create_namespaced_role_binding(namespace=namespace, body=role_binding)
        logger.info(f"✅ Created RoleBinding {role_binding_name}")
    except client.rest.ApiException as e:
        if e.status == 409:
            rbac_api.replace_namespaced_role_binding(
                name=role_binding_name, namespace=namespace, body=role_binding
            )
            logger.info(f"✅ Updated RoleBinding {role_binding_name}")
        else:
            raise

    # Get the ServiceAccount token
    return get_service_account_token(kserve_client, sa_name, namespace)


def get_service_account_token(
    kserve_client: KServeClient,
    sa_name: str,
    namespace: str = KSERVE_TEST_NAMESPACE,
) -> str:
    """Get the token for a ServiceAccount."""
    core_api = kserve_client.core_api

    # Create a token for the ServiceAccount
    audiences = os.getenv("TOKEN_AUDIENCES", "https://kubernetes.default.svc").split(
        ","
    )
    token_request = client.AuthenticationV1TokenRequest(
        spec=client.V1TokenRequestSpec(
            audiences=audiences,
            expiration_seconds=3600,
        )
    )

    try:
        token_response = core_api.create_namespaced_service_account_token(
            name=sa_name,
            namespace=namespace,
            body=token_request,
        )
        logger.info(f"✅ Created token for ServiceAccount {sa_name}")
        return token_response.status.token
    except client.rest.ApiException as e:
        logger.error(f"Failed to create token: {e}")
        raise


def cleanup_service_account(
    kserve_client: KServeClient,
    sa_name: str,
    namespace: str = KSERVE_TEST_NAMESPACE,
):
    """Clean up ServiceAccount, Role, and RoleBinding."""
    core_api = kserve_client.core_api
    rbac_api = client.RbacAuthorizationV1Api()

    role_name = f"{sa_name}-role"
    role_binding_name = f"{sa_name}-binding"

    try:
        rbac_api.delete_namespaced_role_binding(
            name=role_binding_name, namespace=namespace
        )
        logger.info(f"✅ Deleted RoleBinding {role_binding_name}")
    except client.rest.ApiException:
        pass

    try:
        rbac_api.delete_namespaced_role(name=role_name, namespace=namespace)
        logger.info(f"✅ Deleted Role {role_name}")
    except client.rest.ApiException:
        pass

    try:
        core_api.delete_namespaced_service_account(name=sa_name, namespace=namespace)
        logger.info(f"✅ Deleted ServiceAccount {sa_name}")
    except client.rest.ApiException:
        pass


@pytest.mark.llminferenceservice
@pytest.mark.auth
@pytest.mark.asyncio(loop_scope="session")
@pytest.mark.parametrize(
    "test_case",
    [
        pytest.param(
            TestCase(
                base_refs=[
                    "router-managed",
                    "workload-single-cpu",
                    "model-fb-opt-125m",
                ],
                prompt="KServe is a",
                service_name="auth-enabled-test",
            ),
            marks=[
                pytest.mark.cluster_cpu,
                pytest.mark.cluster_single_node,
            ],
            id="auth-enabled-default",
        ),
    ],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_llm_auth_enabled_requires_token(test_case: TestCase):
    """
    Test that when auth is enabled (default):
    - Requests WITH valid token succeed
    - Requests WITHOUT token are rejected (401/403)
    """
    inject_k8s_proxy()

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config"),
        client_configuration=client.Configuration(),
    )

    service_name = test_case.llm_service.metadata.name
    sa_name = f"{service_name}-test-sa"
    test_failed = False

    try:
        # Create LLMInferenceService
        create_llmisvc(kserve_client, test_case.llm_service)
        wait_for_llm_isvc_ready(
            kserve_client, test_case.llm_service, test_case.wait_timeout
        )

        # Create ServiceAccount with GET access
        token = create_service_account_with_get_access(
            kserve_client, sa_name, service_name
        )

        service_url = get_llm_service_url(kserve_client, test_case.llm_service)
        completion_url = f"{service_url}/v1/completions"
        test_payload = {
            "model": test_case.model_name,
            "prompt": test_case.prompt,
            "max_tokens": test_case.max_tokens,
        }

        # Test 1: Request WITHOUT token should fail
        logger.info("Testing request WITHOUT token (should fail)")
        response_no_token = requests.post(
            completion_url,
            headers={"Content-Type": "application/json"},
            json=test_payload,
            timeout=30,
        )
        assert response_no_token.status_code in [
            401,
            403,
        ], f"Expected 401/403 without token, got {response_no_token.status_code}: {response_no_token.text}"
        logger.info(
            f"✅ Request without token rejected: {response_no_token.status_code}"
        )

        # Test 2: Request WITH valid token should succeed
        logger.info("Testing request WITH valid token (should succeed)")
        response_with_token = requests.post(
            completion_url,
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {token}",
            },
            json=test_payload,
            timeout=test_case.response_timeout,
        )
        assert (
            response_with_token.status_code == 200
        ), f"Expected 200 with token, got {response_with_token.status_code}: {response_with_token.text}"
        logger.info("✅ Request with valid token succeeded")

        logger.info("✅ Auth enforcement test passed")

    except Exception as e:
        test_failed = True
        logger.error(f"❌ ERROR: Failed test for {service_name}: {e}")
        _collect_diagnostics(kserve_client, test_case.llm_service)
        raise
    finally:
        try:
            cleanup_service_account(kserve_client, sa_name)

            skip_all_deletion = os.getenv(
                "SKIP_RESOURCE_DELETION", "False"
            ).lower() in (
                "true",
                "1",
                "t",
            )
            skip_deletion_on_failure = os.getenv(
                "SKIP_DELETION_ON_FAILURE", "False"
            ).lower() in (
                "true",
                "1",
                "t",
            )

            should_skip_deletion = skip_all_deletion or (
                skip_deletion_on_failure and test_failed
            )

            if not should_skip_deletion:
                delete_llmisvc(kserve_client, test_case.llm_service)
            elif test_failed and skip_deletion_on_failure:
                logger.info(
                    f"⏭️  Skipping deletion of {service_name} due to test failure (SKIP_DELETION_ON_FAILURE=True)"
                )
        except Exception as e:
            logger.warning(f"⚠️ Warning: Failed to cleanup {service_name}: {e}")


@pytest.mark.llminferenceservice
@pytest.mark.auth
@pytest.mark.asyncio(loop_scope="session")
@pytest.mark.parametrize(
    "test_case",
    [
        pytest.param(
            TestCase(
                base_refs=[
                    "router-managed",
                    "workload-single-cpu",
                    "model-fb-opt-125m",
                ],
                prompt="KServe is a",
                service_name="auth-invalid-token-test",
            ),
            marks=[
                pytest.mark.cluster_cpu,
                pytest.mark.cluster_single_node,
            ],
            id="auth-invalid-token",
        ),
    ],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_llm_auth_invalid_token_rejected(test_case: TestCase):
    """
    Test that when auth is enabled:
    - Requests with MALFORMED tokens are rejected
    """
    inject_k8s_proxy()

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config"),
        client_configuration=client.Configuration(),
    )

    service_name = test_case.llm_service.metadata.name
    sa_name = f"{service_name}-test-sa"
    test_failed = False

    try:
        # Create LLMInferenceService
        create_llmisvc(kserve_client, test_case.llm_service)
        wait_for_llm_isvc_ready(
            kserve_client, test_case.llm_service, test_case.wait_timeout
        )

        # Create ServiceAccount to get a valid token format reference
        create_service_account_with_get_access(kserve_client, sa_name, service_name)

        service_url = get_llm_service_url(kserve_client, test_case.llm_service)
        completion_url = f"{service_url}/v1/completions"
        test_payload = {
            "model": test_case.model_name,
            "prompt": test_case.prompt,
            "max_tokens": test_case.max_tokens,
        }

        # Test 1: Request with MALFORMED token should fail
        logger.info("Testing request with MALFORMED token (should fail)")
        malformed_token = "invalid-malformed-token-xyz123"
        response_malformed = requests.post(
            completion_url,
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {malformed_token}",
            },
            json=test_payload,
            timeout=30,
        )
        assert response_malformed.status_code in [
            401,
            403,
        ], f"Expected 401/403 with malformed token, got {response_malformed.status_code}: {response_malformed.text}"
        logger.info(
            f"✅ Request with malformed token rejected: {response_malformed.status_code}"
        )

        logger.info("✅ Invalid token test passed")

    except Exception as e:
        test_failed = True
        logger.error(f"❌ ERROR: Failed test for {service_name}: {e}")
        _collect_diagnostics(kserve_client, test_case.llm_service)
        raise
    finally:
        try:
            cleanup_service_account(kserve_client, sa_name)

            skip_all_deletion = os.getenv(
                "SKIP_RESOURCE_DELETION", "False"
            ).lower() in (
                "true",
                "1",
                "t",
            )
            skip_deletion_on_failure = os.getenv(
                "SKIP_DELETION_ON_FAILURE", "False"
            ).lower() in (
                "true",
                "1",
                "t",
            )

            should_skip_deletion = skip_all_deletion or (
                skip_deletion_on_failure and test_failed
            )

            if not should_skip_deletion:
                delete_llmisvc(kserve_client, test_case.llm_service)
            elif test_failed and skip_deletion_on_failure:
                logger.info(
                    f"⏭️  Skipping deletion of {service_name} due to test failure (SKIP_DELETION_ON_FAILURE=True)"
                )
        except Exception as e:
            logger.warning(f"⚠️ Warning: Failed to cleanup {service_name}: {e}")


@pytest.mark.llminferenceservice
@pytest.mark.auth
@pytest.mark.asyncio(loop_scope="session")
@pytest.mark.parametrize(
    "test_case",
    [
        pytest.param(
            TestCase(
                base_refs=[
                    "router-auth-disabled",
                    "workload-single-cpu",
                    "model-fb-opt-125m",
                ],
                prompt="KServe is a",
                service_name="auth-disabled-test",
            ),
            marks=[
                pytest.mark.cluster_cpu,
                pytest.mark.cluster_single_node,
            ],
            id="auth-disabled",
        ),
    ],
    indirect=["test_case"],
    ids=generate_test_id,
)
@log_execution
def test_llm_auth_disabled_no_token_required(test_case: TestCase):
    """
    Test that when auth is disabled via annotation:
    - Requests WITHOUT token succeed
    """
    inject_k8s_proxy()

    kserve_client = KServeClient(
        config_file=os.environ.get("KUBECONFIG", "~/.kube/config"),
        client_configuration=client.Configuration(),
    )

    service_name = test_case.llm_service.metadata.name
    test_failed = False

    # Add annotation to disable auth
    if not test_case.llm_service.metadata.annotations:
        test_case.llm_service.metadata.annotations = {}
    test_case.llm_service.metadata.annotations[
        "security.opendatahub.io/enable-auth"
    ] = "false"

    try:
        # Create LLMInferenceService
        create_llmisvc(kserve_client, test_case.llm_service)
        wait_for_llm_isvc_ready(
            kserve_client, test_case.llm_service, test_case.wait_timeout
        )

        service_url = get_llm_service_url(kserve_client, test_case.llm_service)
        completion_url = f"{service_url}/v1/completions"
        test_payload = {
            "model": test_case.model_name,
            "prompt": test_case.prompt,
            "max_tokens": test_case.max_tokens,
        }

        # Test: Request WITHOUT token should succeed when auth is disabled
        logger.info("Testing request WITHOUT token (should succeed when auth disabled)")
        response_no_token = requests.post(
            completion_url,
            headers={"Content-Type": "application/json"},
            json=test_payload,
            timeout=test_case.response_timeout,
        )
        assert (
            response_no_token.status_code == 200
        ), f"Expected 200 without token when auth disabled, got {response_no_token.status_code}: {response_no_token.text}"
        logger.info("✅ Request without token succeeded (auth disabled)")

        logger.info("✅ Auth disabled test passed")

    except Exception as e:
        test_failed = True
        logger.error(f"❌ ERROR: Failed test for {service_name}: {e}")
        _collect_diagnostics(kserve_client, test_case.llm_service)
        raise
    finally:
        try:
            skip_all_deletion = os.getenv(
                "SKIP_RESOURCE_DELETION", "False"
            ).lower() in (
                "true",
                "1",
                "t",
            )
            skip_deletion_on_failure = os.getenv(
                "SKIP_DELETION_ON_FAILURE", "False"
            ).lower() in (
                "true",
                "1",
                "t",
            )

            should_skip_deletion = skip_all_deletion or (
                skip_deletion_on_failure and test_failed
            )

            if not should_skip_deletion:
                delete_llmisvc(kserve_client, test_case.llm_service)
            elif test_failed and skip_deletion_on_failure:
                logger.info(
                    f"⏭️  Skipping deletion of {service_name} due to test failure (SKIP_DELETION_ON_FAILURE=True)"
                )
        except Exception as e:
            logger.warning(f"⚠️ Warning: Failed to cleanup {service_name}: {e}")
