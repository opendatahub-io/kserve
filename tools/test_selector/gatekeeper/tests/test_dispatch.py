from __future__ import annotations

import json
import inspect
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from test_selector.gatekeeper.dispatch import (
    APIError,
    APP_BOT_LOGIN,
    Dispatcher,
    GitHubAppClient,
    MAX_STATUS_PAGES,
    SelectionError,
    UrllibHTTP,
    build_parser,
    validate_selection,
)


SHA = "a" * 40
OTHER_SHA = "b" * 40
ALL = ["e2e-graph", "e2e-raw", "e2e-predictor", "e2e-llm-inference-service"]


def selection(*jobs: str, mode: str = "selected") -> dict:
    return {
        "schema_version": 1,
        "repository": "opendatahub-io/kserve",
        "pull_number": 7,
        "base_sha": "c" * 40,
        "head_sha": SHA,
        "mode": mode,
        "jobs": list(jobs),
        "changed_files": ["pkg/example.go"],
        "reasons": ["example"],
    }


def test_trusted_job_map_has_exact_poc_commands_contexts_targets_and_expressions():
    job_map = json.loads(
        Path(__file__).parents[1].joinpath("ocp_jobs.json").read_text()
    )
    assert job_map == {
        "e2e-graph": {
            "command": "/test e2e-graph",
            "context": "ci/prow/e2e-graph",
            "target": "e2e-graph-ocp",
            "expression": "graph",
        },
        "e2e-raw": {
            "command": "/test e2e-raw",
            "context": "ci/prow/e2e-raw",
            "target": "e2e-raw-ocp",
            "expression": "raw or rawcipn",
        },
        "e2e-predictor": {
            "command": "/test e2e-predictor",
            "context": "ci/prow/e2e-predictor",
            "target": "e2e-predictor-ocp",
            "expression": "predictor or kserve_on_openshift",
        },
        "e2e-llm-inference-service": {
            "command": "/test e2e-llm-inference-service",
            "context": "ci/prow/e2e-llm-inference-service",
            "target": "e2e-llmisvc-ocp",
            "expression": "llmisvc_core and cluster_cpu and not pvc_storage",
        },
    }


def test_shorthand_job_map_is_rejected(tmp_path: Path):
    shorthand = tmp_path / "ocp_jobs.json"
    shorthand.write_text(json.dumps({"e2e-graph": "graph"}))
    with pytest.raises(SelectionError):
        Dispatcher(FakeGitHub(), job_file=shorthand)


@pytest.mark.parametrize("field", ["command", "context", "target", "expression"])
def test_trusted_job_map_rejects_noncanonical_locked_record(tmp_path: Path, field: str):
    path = tmp_path / "ocp_jobs.json"
    records = json.loads(
        Path(__file__).parents[1].joinpath("ocp_jobs.json").read_text()
    )
    records["e2e-graph"][field] = "attacker-controlled"
    path.write_text(json.dumps(records))

    with pytest.raises(
        SelectionError, match="does not match the locked job definition"
    ):
        Dispatcher(FakeGitHub(), job_file=path)


class FakeGitHub:
    def __init__(self, *, statuses=None, head=SHA, status_sequence=None):
        self.head = head
        self.statuses_value = statuses or []
        self.status_sequence = list(status_sequence or [])
        self.requests = []
        self.next_comment = 100

    def request(self, method, path, *, payload=None):
        self.requests.append((method, path, payload))
        if method == "GET" and "/pulls/" in path:
            return {"head": {"sha": self.head}, "base": {"ref": "master"}}
        if method == "GET" and "/status" in path:
            statuses = (
                self.status_sequence.pop(0)
                if self.status_sequence
                else self.statuses_value
            )
            return {"statuses": statuses}
        if method == "POST" and "/comments" in path:
            self.next_comment += 1
            return {"id": self.next_comment, "created_at": "2026-01-01T00:00:00Z"}
        if method == "PATCH" and "/comments/" in path:
            return {"id": 101}
        raise AssertionError((method, path, payload))


def prow(name, state, *, when="2026-01-01T00:00:01Z", target=True):
    return (
        {
            "context": f"ci/prow/{name}",
            "state": state,
            "target_url": f"https://prow.ci.openshift.org/view/gcs/{name}",
            "created_at": when,
            "updated_at": when,
        }
        if target
        else {
            "context": f"ci/prow/{name}",
            "state": state,
            "target_url": "https://example.invalid/not-prow",
        }
    )


def test_valid_selection_is_canonical_and_malformed_valid_selection_widens():
    normalized = validate_selection(selection("e2e-predictor", "e2e-graph"))
    assert normalized["jobs"] == ["e2e-graph", "e2e-predictor"]

    malformed = selection("e2e-graph", "e2e-graph")
    widened = validate_selection(malformed)
    assert widened["mode"] == "fallback-all"
    assert widened["jobs"] == ALL

    malformed["jobs"] = [{}]
    assert validate_selection(malformed)["jobs"] == ALL

    malformed["jobs"] = ["e2e-graph"]
    malformed["unexpected"] = True
    normalized = validate_selection(malformed)
    assert normalized["jobs"] == ALL
    assert "unexpected" not in normalized



@pytest.mark.parametrize("schema_version", [None, 2, "1", True])
def test_malformed_schema_version_widens_valid_identity_to_all_jobs(schema_version):
    malformed = selection("e2e-graph")
    malformed["schema_version"] = schema_version

    normalized = validate_selection(malformed)

    assert normalized["schema_version"] == 1
    assert normalized["mode"] == "fallback-all"
    assert normalized["jobs"] == ALL


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("changed_files", [1, "valid-path"]),
        ("changed_files", "not-an-array"),
        ("reasons", [1, "valid reason"]),
        ("reasons", "not-an-array"),
    ],
)
def test_malformed_diagnostics_widen_without_crashing(field, value):
    malformed = selection("e2e-graph")
    malformed[field] = value

    normalized = validate_selection(malformed)

    assert normalized["mode"] == "fallback-all"
    assert normalized["jobs"] == ALL
    assert all(isinstance(item, str) for item in normalized[field])


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("changed_files", ["p" * 501]),
        ("changed_files", ["pkg/example.go"] * 201),
        ("reasons", ["r" * 501]),
        ("reasons", ["example"] * 201),
    ],
)
def test_schema_bound_violations_widen_to_all_jobs(field, value):
    malformed = selection("e2e-graph")
    malformed[field] = value

    normalized = validate_selection(malformed)

    assert normalized["mode"] == "fallback-all"
    assert normalized["jobs"] == ALL
    assert "malformed selection widened to all jobs" in normalized["reasons"]
    assert len(normalized["changed_files"]) <= 200
    assert len(normalized["reasons"]) <= 200
    assert all(len(item) <= 500 for item in normalized["changed_files"])
    assert all(len(item) <= 500 for item in normalized["reasons"])



@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("repository", "attacker/repository"),
        ("pull_number", 0),
        ("base_sha", "not-a-sha"),
        ("head_sha", "not-a-sha"),
    ],
)
def test_identity_errors_remain_fatal_with_malformed_schema_version(field, value):
    malformed = selection("e2e-graph")
    malformed["schema_version"] = 999
    malformed[field] = value

    with pytest.raises(SelectionError):
        validate_selection(malformed)


def test_invalid_identity_fails_before_any_github_request():
    fake = FakeGitHub()
    bad = selection("e2e-graph")
    bad["head_sha"] = "not-a-sha"
    result = Dispatcher(fake).run(bad)
    assert result.status == "failure"
    assert fake.requests == []


def test_live_base_can_advance_after_evaluation_but_head_must_match():
    class BaseChanging(FakeGitHub):
        def request(self, method, path, *, payload=None):
            value = super().request(method, path, payload=payload)
            if method == "GET" and "/pulls/" in path:
                value["base"] = {"ref": "master", "sha": "d" * 40}
            return value

    fake = BaseChanging()
    fake.statuses_value = [prow("e2e-graph", "success")]
    result = Dispatcher(fake).run(selection("e2e-graph"))
    assert result.status == "success"
    assert not any(request[0] == "POST" for request in fake.requests)


def test_dispatcher_reads_wanted_status_from_second_combined_status_page():
    class Paginated(FakeGitHub):
        def request(self, method, path, *, payload=None):
            if method == "GET" and "/status" in path:
                self.requests.append((method, path, payload))
                page = path.rsplit("page=", 1)[-1]
                if page == "1":
                    return {"statuses": [prow("unrelated", "success")] * 100}
                if page == "2":
                    return {"statuses": [prow("e2e-graph", "success")]}
                return {"statuses": []}
            return super().request(method, path, payload=payload)

    fake = Paginated()
    statuses = Dispatcher(fake)._statuses(SHA)

    assert any(item["context"] == "ci/prow/e2e-graph" for item in statuses)
    assert [path for _, path, _ in fake.requests] == [
        f"/repos/opendatahub-io/kserve/commits/{SHA}/status?per_page=100&page=1",
        f"/repos/opendatahub-io/kserve/commits/{SHA}/status?per_page=100&page=2",
    ]


def test_status_page_exhaustion_fails_closed_without_dispatching():
    class Exhausted(FakeGitHub):
        def request(self, method, path, *, payload=None):
            if method == "GET" and "/status" in path:
                self.requests.append((method, path, payload))
                return {"statuses": [prow("unrelated", "success")] * 100}
            return super().request(method, path, payload=payload)

    fake = Exhausted()
    result = Dispatcher(fake).run(selection("e2e-graph"))

    assert result.status == "failure"
    assert "pagination exhausted" in result.reason
    assert len([path for _, path, _ in fake.requests if "/status" in path]) == (
        MAX_STATUS_PAGES
    )
    assert not any(method == "POST" for method, _, _ in fake.requests)


def test_empty_selection_succeeds_without_a_comment(tmp_path: Path):
    fake = FakeGitHub()
    result = Dispatcher(fake).run(selection(), artifact_dir=tmp_path)
    assert result.status == "success"
    assert not any(request[0] in {"POST", "PATCH"} for request in fake.requests)
    assert (
        json.loads((tmp_path / "aggregation.json").read_text())["status"] == "success"
    )
    assert (tmp_path / "junit.xml").exists()


def test_reuses_success_and_pending_without_duplicate_comment(tmp_path: Path):
    fake = FakeGitHub(statuses=[prow("e2e-graph", "success")])
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))
    assert result.status == "success"
    assert not any(request[0] == "POST" for request in fake.requests)

    pending = FakeGitHub(statuses=[prow("e2e-graph", "pending")])
    # A zero aggregate timeout makes the existing pending path deterministic.
    result = Dispatcher(pending, poll_interval=0, aggregate_timeout=0).run(
        selection("e2e-graph"), artifact_dir=tmp_path
    )
    assert result.status == "failure"
    assert not any(request[0] == "POST" for request in pending.requests)
    assert 'failures="1"' in (tmp_path / "junit.xml").read_text()


def test_missing_status_posts_exact_allowlisted_comment_then_patches_final_result():
    fake = FakeGitHub(
        status_sequence=[
            [],
            [prow("e2e-graph", "pending")],
            [prow("e2e-graph", "success")],
        ],
    )
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))
    post = next(request for request in fake.requests if request[0] == "POST")
    assert (
        post[2]["body"] == f"<!-- kserve-test-selector head={SHA} -->\n/test e2e-graph"
    )
    assert result.status == "success"
    patch = next(request for request in fake.requests if request[0] == "PATCH")
    assert "kserve-test-selector: success" in patch[2]["body"]


def test_reconciles_unfinished_same_head_marker_comment_from_this_app():
    class CommentAware(FakeGitHub):
        def comments(self, repository, pull_number):
            self.requests.append(
                ("GET", f"/repos/{repository}/issues/{pull_number}/comments", None)
            )
            return [
                {
                    "id": 101,
                    "created_at": "2026-01-01T00:00:00Z",
                    "user": {"login": APP_BOT_LOGIN},
                    "body": f"<!-- kserve-test-selector head={SHA} -->\n/test e2e-graph",
                }
            ]

    fake = CommentAware(status_sequence=[[], [prow("e2e-graph", "success")]])
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))

    assert result.status == "success"
    assert not any(request[0] == "POST" for request in fake.requests)
    patch = next(request for request in fake.requests if request[0] == "PATCH")
    assert "kserve-test-selector: success" in patch[2]["body"]


@pytest.mark.parametrize(
    ("login", "body"),
    [
        (
            "another-integration[bot]",
            f"<!-- kserve-test-selector head={SHA} -->\n/test e2e-graph",
        ),
        (
            APP_BOT_LOGIN,
            f"<!-- kserve-test-selector head={SHA} -->\n/test e2e-graph\n\nkserve-test-selector: failure",
        ),
        (
            APP_BOT_LOGIN,
            f"<!-- kserve-test-selector head={'b' * 40} -->\n/test e2e-graph",
        ),
    ],
)
def test_reconciliation_never_reuses_foreign_completed_or_wrong_head_comment(
    login: str, body: str
):
    class CommentAware(FakeGitHub):
        def comments(self, repository, pull_number):
            self.requests.append(
                ("GET", f"/repos/{repository}/issues/{pull_number}/comments", None)
            )
            return [
                {
                    "id": 101,
                    "created_at": "2026-01-01T00:00:00Z",
                    "user": {"login": login},
                    "body": body,
                }
            ]

    fake = CommentAware(status_sequence=[[], [prow("e2e-graph", "success")]])
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))

    assert result.status == "success"
    assert any(request[0] == "POST" for request in fake.requests)


@pytest.mark.parametrize("state", ["failure", "error"])
def test_fresh_terminal_failure_or_error_fails_and_patches_comment(state):
    fake = FakeGitHub(status_sequence=[[], [prow("e2e-graph", state)]])
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))
    assert result.status == "failure"
    patch = next(request for request in fake.requests if request[0] == "PATCH")
    assert "kserve-test-selector: failure" in patch[2]["body"]


def test_latest_duplicate_context_is_the_only_status_considered():
    old = prow("e2e-graph", "failure", when="2026-01-01T00:00:01Z")
    latest = prow("e2e-graph", "success", when="2026-01-01T00:00:02Z")
    fake = FakeGitHub(statuses=[old, latest])
    result = Dispatcher(fake).run(selection("e2e-graph"))
    assert result.status == "success"
    assert not any(request[0] == "POST" for request in fake.requests)


def test_duplicate_context_with_equal_timestamps_keeps_first_response_item():
    first = prow("e2e-graph", "failure", when="2026-01-01T00:00:01Z")
    second = prow("e2e-graph", "success", when="2026-01-01T00:00:01Z")
    dispatcher = Dispatcher(FakeGitHub())

    classified = dispatcher._classify([first, second], selection("e2e-graph"))

    assert classified["e2e-graph"]["state"] == "failure"


def test_duplicate_context_with_missing_timestamps_keeps_first_response_item():
    first = prow("e2e-graph", "failure")
    second = prow("e2e-graph", "success")
    del first["created_at"]
    del first["updated_at"]
    del second["created_at"]
    del second["updated_at"]
    dispatcher = Dispatcher(FakeGitHub())

    classified = dispatcher._classify([first, second], selection("e2e-graph"))

    assert classified["e2e-graph"]["state"] == "failure"


def test_duplicate_context_with_later_timestamp_replaces_first_response_item():
    first = prow("e2e-graph", "failure", when="2026-01-01T00:00:01Z")
    later = prow("e2e-graph", "success", when="2026-01-01T00:00:02Z")
    dispatcher = Dispatcher(FakeGitHub())

    classified = dispatcher._classify([first, later], selection("e2e-graph"))

    assert classified["e2e-graph"]["state"] == "success"


def test_head_change_during_polling_fails_and_patches_command_comment():
    class HeadChanges(FakeGitHub):
        def __init__(self):
            super().__init__(status_sequence=[[]])
            self.pull_count = 0

        def request(self, method, path, *, payload=None):
            if method == "GET" and "/pulls/" in path:
                self.pull_count += 1
                response = {
                    "head": {"sha": SHA if self.pull_count == 1 else OTHER_SHA},
                    "base": {"ref": "master"},
                }
                self.requests.append((method, path, payload))
                return response
            return super().request(method, path, payload=payload)

    fake = HeadChanges()
    result = Dispatcher(fake, poll_interval=0, head_check_interval=0).run(
        selection("e2e-graph")
    )
    assert result.status == "failure"
    assert "head changed" in result.reason
    assert any(request[0] == "PATCH" for request in fake.requests)


def test_head_change_is_rechecked_before_returning_success():
    class HeadChangesAtFinalCheck(FakeGitHub):
        def __init__(self):
            super().__init__(statuses=[prow("e2e-graph", "success")])
            self.pull_count = 0

        def request(self, method, path, *, payload=None):
            if method == "GET" and "/pulls/" in path:
                self.pull_count += 1
                self.requests.append((method, path, payload))
                return {
                    "head": {"sha": SHA if self.pull_count == 1 else OTHER_SHA},
                    "base": {"ref": "master"},
                }
            return super().request(method, path, payload=payload)

    fake = HeadChangesAtFinalCheck()
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))

    assert result.status == "failure"
    assert "head changed" in result.reason
    assert fake.pull_count == 2


def test_retargeted_live_base_ref_fails_before_dispatch():
    class Retargeted(FakeGitHub):
        def request(self, method, path, *, payload=None):
            response = super().request(method, path, payload=payload)
            if method == "GET" and "/pulls/" in path:
                response["base"]["ref"] = "release"
            return response

    fake = Retargeted()
    result = Dispatcher(fake).run(selection("e2e-graph"))

    assert result.status == "failure"
    assert "base ref" in result.reason
    assert not any(request[0] == "POST" for request in fake.requests)


def test_comment_api_failure_fails_without_attempting_a_patch():
    class MissingCommentID(FakeGitHub):
        def request(self, method, path, *, payload=None):
            if method == "POST" and "/comments" in path:
                self.requests.append((method, path, payload))
                return {"created_at": "2026-01-01T00:00:00Z"}
            return super().request(method, path, payload=payload)

    fake = MissingCommentID(status_sequence=[[]])
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))
    assert result.status == "failure"
    assert "comment id" in result.reason
    assert not any(request[0] == "PATCH" for request in fake.requests)


def test_final_comment_keeps_only_commands_that_were_dispatched():
    fake = FakeGitHub(
        status_sequence=[
            [prow("e2e-graph", "success")],
            [prow("e2e-graph", "success"), prow("e2e-predictor", "success")],
        ]
    )
    result = Dispatcher(fake, poll_interval=0).run(
        selection("e2e-graph", "e2e-predictor")
    )
    assert result.status == "success"
    patch = next(request for request in fake.requests if request[0] == "PATCH")
    assert "/test e2e-predictor" in patch[2]["body"]
    assert "/test e2e-graph" not in patch[2]["body"]


def test_failed_status_is_dispatched_and_stale_terminal_result_is_ignored():
    stale = prow("e2e-graph", "failure", when="2025-01-01T00:00:00Z")
    fake = FakeGitHub(
        status_sequence=[[stale], [stale], [prow("e2e-graph", "success")]]
    )
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))
    assert result.status == "success"
    assert any(request[0] == "POST" for request in fake.requests)


def test_wrong_target_url_is_not_a_reusable_status():
    fake = FakeGitHub(
        status_sequence=[
            [prow("e2e-graph", "success", target=False)],
            [prow("e2e-graph", "success")],
        ]
    )
    result = Dispatcher(fake, poll_interval=0).run(selection("e2e-graph"))
    assert result.status == "success"
    assert any(request[0] == "POST" for request in fake.requests)


def test_llmisvc_selection_uses_the_locked_pseudonymous_test_command():
    fake = FakeGitHub(statuses=[])
    # Keep the test quick: an absent status is a dispatch failure after zero
    # seconds, but the command body is still observable.
    result = Dispatcher(fake, appearance_timeout=0, poll_interval=0).run(
        selection("e2e-llm-inference-service")
    )
    post = next(request for request in fake.requests if request[0] == "POST")
    assert "/test e2e-llm-inference-service" in post[2]["body"]
    assert result.status == "failure"


def test_api_client_refreshes_installation_token_after_401():
    class Response:
        def __init__(self, status, body):
            self.status_code = status
            self.body = body

        def json(self):
            return self.body

    class HTTP:
        def __init__(self):
            self.calls = []

        def request(self, method, url, *, headers, json=None):
            self.calls.append((method, url, headers, json))
            if url.endswith("/access_tokens"):
                return Response(
                    201,
                    {
                        "token": f"token-{len(self.calls)}",
                        "expires_at": "2099-01-01T00:00:00Z",
                    },
                )
            if (
                len(
                    [
                        call
                        for call in self.calls
                        if not call[1].endswith("/access_tokens")
                    ]
                )
                == 1
            ):
                return Response(401, {})
            return Response(200, {"head": {"sha": SHA}})

    # A generated key is unnecessary here: replace JWT creation at the public
    # module seam so the test remains about renewal rather than cryptography.
    import test_selector.gatekeeper.dispatch as dispatch

    original = dispatch._jwt
    dispatch._jwt = lambda app, key, now: "jwt"
    try:
        http = HTTP()
        client = GitHubAppClient(
            http, app_id="1", installation_id="2", private_key="key"
        )
        body = client.pull("opendatahub-io/kserve", 7)
        assert body["head"]["sha"] == SHA
        access = [call for call in http.calls if call[1].endswith("/access_tokens")]
        assert len(access) == 2
    finally:
        dispatch._jwt = original


def test_api_client_reads_wanted_status_from_second_combined_status_page():
    class HTTP:
        def __init__(self):
            self.calls = []

        def request(self, method, url, *, headers, json=None):
            self.calls.append((method, url))
            page = url.rsplit("page=", 1)[-1]
            if page == "1":
                statuses = [prow("unrelated", "success")] * 100
            elif page == "2":
                statuses = [prow("e2e-graph", "success")]
            else:
                statuses = []
            return 200, {"statuses": statuses}, {}

    http = HTTP()
    client = GitHubAppClient(http)
    client._token = "fixture-token"
    client._token_expires_at = float("inf")

    statuses = client.statuses("opendatahub-io/kserve", SHA)

    assert any(item["context"] == "ci/prow/e2e-graph" for item in statuses)
    assert [url for _, url in http.calls] == [
        f"https://api.github.com/repos/opendatahub-io/kserve/commits/{SHA}/status?per_page=100&page=1",
        f"https://api.github.com/repos/opendatahub-io/kserve/commits/{SHA}/status?per_page=100&page=2",
    ]


def test_api_client_reads_marker_comment_from_second_page():
    marker = {
        "id": 101,
        "body": f"<!-- kserve-test-selector head={SHA} -->\n/test e2e-graph",
    }

    class HTTP:
        def __init__(self):
            self.calls = []

        def request(self, method, url, *, headers, json=None):
            self.calls.append((method, url))
            page = url.rsplit("page=", 1)[-1]
            if page == "1":
                comments = [{"id": number} for number in range(100)]
            else:
                comments = [marker]
            return 200, comments, {}

    http = HTTP()
    client = GitHubAppClient(http)
    client._token = "fixture-token"
    client._token_expires_at = float("inf")

    comments = client.comments("opendatahub-io/kserve", 7)

    assert marker in comments
    assert [url for _, url in http.calls] == [
        "https://api.github.com/repos/opendatahub-io/kserve/issues/7/comments?per_page=100&page=1",
        "https://api.github.com/repos/opendatahub-io/kserve/issues/7/comments?per_page=100&page=2",
    ]


@pytest.mark.parametrize("operation", ["comment", "patch_comment"])
def test_api_client_refreshes_after_unauthorized_comment_mutation(
    operation: str, monkeypatch: pytest.MonkeyPatch
) -> None:
    class HTTP:
        def __init__(self) -> None:
            self.token_requests = 0
            self.mutations = 0

        def request(self, method, url, *, headers, json=None):
            if url.endswith("/access_tokens"):
                self.token_requests += 1
                return (
                    201,
                    {
                        "token": f"token-{self.token_requests}",
                        "expires_at": "2099-01-01T00:00:00Z",
                    },
                    {},
                )
            self.mutations += 1
            return (401, {}, {}) if self.mutations == 1 else (200, {"id": 101}, {})

    import test_selector.gatekeeper.dispatch as dispatch

    monkeypatch.setattr(dispatch, "_jwt", lambda app, key, now: "jwt")
    http = HTTP()
    client = GitHubAppClient(http, app_id="1", installation_id="2", private_key="key")

    if operation == "comment":
        client.comment("opendatahub-io/kserve", 7, "body")
    else:
        client.patch_comment("opendatahub-io/kserve", 101, "body")

    assert http.token_requests == 2
    assert http.mutations == 2


def test_api_client_backs_off_transient_failures_without_exposing_token():
    sleeps = []

    class HTTP:
        def __init__(self):
            self.count = 0

        def request(self, method, url, *, headers, json=None):
            self.count += 1
            assert "secret-token" not in str((method, url, json))
            if self.count < 3:
                return (503, {}, {"Retry-After": "2"})
            return (200, {"head": {"sha": SHA}}, {})

    client = GitHubAppClient(HTTP(), sleeper=sleeps.append)
    client._token = "secret-token"
    client._token_expires_at = float("inf")
    assert client.pull("opendatahub-io/kserve", 7)["head"]["sha"] == SHA
    assert sleeps == [2.0, 2.0]


@pytest.mark.parametrize("operation", ["comment", "patch_comment"])
def test_api_client_never_retries_ambiguous_comment_mutations(operation: str) -> None:
    class HTTP:
        def __init__(self):
            self.calls = 0

        def request(self, method, url, *, headers, json=None):
            self.calls += 1
            return (503, {}, {})

    http = HTTP()
    client = GitHubAppClient(http, max_retries=3)
    client._token = "fixture-token"
    client._token_expires_at = float("inf")

    with pytest.raises(APIError):
        if operation == "comment":
            client.comment("opendatahub-io/kserve", 7, "body")
        else:
            client.patch_comment("opendatahub-io/kserve", 101, "body")

    assert http.calls == 1


def test_api_client_retries_transient_installation_token_acquisition(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    sleeps: list[float] = []

    class HTTP:
        def __init__(self):
            self.calls: list[str] = []

        def request(self, method, url, *, headers, json=None):
            self.calls.append(url)
            if url.endswith("/access_tokens") and self.calls.count(url) == 1:
                return (503, {}, {})
            if url.endswith("/access_tokens"):
                return (
                    201,
                    {"token": "fixture-token", "expires_at": "2099-01-01T00:00:00Z"},
                    {},
                )
            return (200, {"head": {"sha": SHA}}, {})

    monkeypatch.setattr(
        "test_selector.gatekeeper.dispatch._jwt", lambda app, key, now: "fixture-jwt"
    )
    http = HTTP()
    client = GitHubAppClient(
        http,
        app_id="fixture-app",
        installation_id="22",
        private_key="fixture-key",
        sleeper=sleeps.append,
    )

    assert client.pull("opendatahub-io/kserve", 7)["head"]["sha"] == SHA
    assert (
        http.calls.count("https://api.github.com/app/installations/22/access_tokens")
        == 2
    )
    assert sleeps == [1]


def test_urllib_transport_exercises_app_pull_status_comment_and_patch(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[str, str, str]] = []
    status_calls = 0
    installation_token = "fixture-installation-token"

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, format: str, *args) -> None:
            return

        def _body(self) -> dict:
            length = int(self.headers.get("Content-Length", "0"))
            return json.loads(self.rfile.read(length) or b"{}")

        def _reply(self, status: int, body: dict) -> None:
            payload = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def do_POST(self) -> None:
            body = self._body()
            calls.append(("POST", self.path, json.dumps(body, sort_keys=True)))
            if self.path == "/app/installations/22/access_tokens":
                assert self.headers["Authorization"] == "Bearer fixture-jwt"
                self._reply(
                    201,
                    {"token": installation_token, "expires_at": "2099-01-01T00:00:00Z"},
                )
                return
            assert self.path == "/repos/opendatahub-io/kserve/issues/7/comments"
            assert self.headers["Authorization"] == f"token {installation_token}"
            self._reply(201, {"id": 101, "created_at": "2026-01-01T00:00:02Z"})

        def do_GET(self) -> None:
            nonlocal status_calls
            calls.append(("GET", self.path, ""))
            assert self.headers["Authorization"] == f"token {installation_token}"
            if self.path == "/repos/opendatahub-io/kserve/pulls/7":
                self._reply(200, {"head": {"sha": SHA}, "base": {"ref": "master"}})
                return
            if (
                self.path
                == "/repos/opendatahub-io/kserve/issues/7/comments?per_page=100&page=1"
            ):
                self._reply(200, [])
                return
            assert (
                self.path
                == f"/repos/opendatahub-io/kserve/commits/{SHA}/status?per_page=100&page=1"
            )
            status_calls += 1
            state = "pending" if status_calls == 1 else "success"
            statuses = (
                []
                if status_calls == 1
                else [prow("e2e-graph", state, when="2099-01-01T00:00:03Z")]
            )
            self._reply(200, {"statuses": statuses})

        def do_PATCH(self) -> None:
            body = self._body()
            calls.append(("PATCH", self.path, json.dumps(body, sort_keys=True)))
            assert self.path == "/repos/opendatahub-io/kserve/issues/comments/101"
            assert self.headers["Authorization"] == f"token {installation_token}"
            self._reply(200, {"id": 101})

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    monkeypatch.setattr(
        "test_selector.gatekeeper.dispatch._jwt", lambda app, key, now: "fixture-jwt"
    )
    try:
        client = GitHubAppClient(
            UrllibHTTP(),
            app_id="fixture-app",
            installation_id="22",
            private_key="fixture-private-key",
            base_url=f"http://127.0.0.1:{server.server_port}",
        )
        result = Dispatcher(client, poll_interval=0).run(selection("e2e-graph"))
    finally:
        server.shutdown()
        thread.join(timeout=2)
        server.server_close()

    assert result.status == "success"
    assert [method for method, _, _ in calls] == [
        "POST",
        "GET",
        "GET",
        "GET",
        "POST",
        "GET",
        "GET",
        "PATCH",
    ]
    assert any(path.endswith("/access_tokens") for _, path, _ in calls)
    assert any(path.endswith("/pulls/7") for _, path, _ in calls)
    assert any("/status?per_page=100&page=" in path for _, path, _ in calls)
    assert any(
        method == "POST" and path.endswith("/comments") for method, path, _ in calls
    )
    assert any(
        method == "PATCH" and path.endswith("/comments/101")
        for method, path, _ in calls
    )
    assert installation_token not in json.dumps(result.to_dict())


def test_production_cli_does_not_accept_personal_access_tokens():
    with pytest.raises(SystemExit):
        build_parser().parse_args(
            ["--selection", "selection.json", "--token", "personal-token"]
        )

    from test_selector.gatekeeper.dispatch import dispatch

    assert "access_token" not in inspect.signature(GitHubAppClient).parameters
    assert "access_token" not in inspect.signature(Dispatcher).parameters
    assert "access_token" not in inspect.signature(dispatch).parameters


@pytest.mark.parametrize("option", ["--repo", "--base-url"])
def test_production_cli_does_not_accept_repository_or_api_overrides(option: str):
    with pytest.raises(SystemExit):
        build_parser().parse_args(
            ["--selection", "selection.json", option, "https://attacker.invalid"]
        )
