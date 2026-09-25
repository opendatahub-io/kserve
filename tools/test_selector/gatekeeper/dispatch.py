"""Trusted GitHub-App dispatcher and aggregator for the ODH Gatekeeper.

The module intentionally has no dependency on the selector.  The selector's
JSON is an untrusted boundary: it is validated, normalised, and then used only
to choose from the small, package-owned job map in :mod:`ocp_jobs.json`.

``Dispatcher`` is the public interface.  Its HTTP client, clock, and sleeper
are constructor arguments, which makes the complete state machine testable
without a GitHub credential or a network connection.
"""

from __future__ import annotations

import argparse
import base64
import datetime as _datetime
import json
import json as _json
import os
import re
import sys
import time
import xml.etree.ElementTree as ET
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Iterable, Mapping, Sequence
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

try:  # Optional at import time; production images include cryptography.
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import padding
except ImportError:  # pragma: no cover - exercised only in minimal images.
    hashes = serialization = padding = None


REPOSITORY = "opendatahub-io/kserve"
SCHEMA_VERSION = 1
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
PROW_URL_PREFIX = "https://prow.ci.openshift.org/"
# Combined commit statuses are paginated independently of the total status
# count.  Keep the bound finite so an unexpectedly large response cannot make
# the dispatcher loop forever, and fail closed if every page is full.
STATUS_PAGE_SIZE = 100
MAX_STATUS_PAGES = 10
# Issue comments need the same bounded pagination: the idempotency marker may
# predate the first page on a busy pull request.
COMMENT_PAGE_SIZE = 100
MAX_COMMENT_PAGES = 10
DIAGNOSTIC_MAX_ITEMS = 200
DIAGNOSTIC_MAX_LENGTH = 500
COMMENT_MARKER = "<!-- kserve-test-selector head={head} -->"
# GitHub renders an installation token's author as the App's fixed bot login.
# Reconciliation is limited to comments authored by this exact identity; a
# matching marker from a human or another integration is never mutated.
APP_BOT_LOGIN = "odh-kserve-test-selector[bot]"
ALLOWLISTED_JOBS = (
    "e2e-graph",
    "e2e-raw",
    "e2e-predictor",
    "e2e-llm-inference-service",
)
ALLOWED_JOBS = ALLOWLISTED_JOBS
TERMINAL_STATES = {
    "success",
    "failure",
    "error",
    "cancelled",
    "neutral",
    "skipped",
    "aborted",
    "terminated",
}
UNSUCCESSFUL_STATES = TERMINAL_STATES - {"success"}

DEFAULT_JOB_FILE = Path(__file__).with_name("ocp_jobs.json")
LOCKED_JOB_MAP = {
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


class DispatchError(RuntimeError):
    """An expected, user-visible Gatekeeper failure."""


class SelectionError(DispatchError):
    """The selection has an invalid identity or cannot be normalised."""


class APIError(DispatchError):
    """GitHub returned an unsuccessful response after retry policy."""


def _job_map(path: str | os.PathLike[str] | None = None) -> dict[str, dict[str, str]]:
    """Read the trusted job map.

    The map is a package-owned boundary. Every entry must explicitly carry its
    command, context, target, and selector expression; no selector input can
    cause command or target inference.
    """

    raw = json.loads(Path(path or DEFAULT_JOB_FILE).read_text(encoding="utf-8"))
    if not isinstance(raw, Mapping):
        raise SelectionError("trusted job map is not an object")
    if set(raw) != set(ALLOWLISTED_JOBS):
        raise SelectionError("trusted job map does not contain the exact allowlist")
    records = raw.items()

    result: dict[str, dict[str, str]] = {}
    for name, record in records:
        if not isinstance(name, str):
            raise SelectionError("trusted job map contains an invalid record")
        if not isinstance(record, Mapping):
            raise SelectionError("trusted job map contains an invalid record")
        if set(record) != {"command", "context", "target", "expression"}:
            raise SelectionError(f"trusted job map entry {name!r} is incomplete")
        command = record["command"]
        context = record["context"]
        target = record["target"]
        expression = record["expression"]
        if not all(
            isinstance(value, str) for value in (command, context, target, expression)
        ):
            raise SelectionError(f"trusted job map entry {name!r} is incomplete")
        if {
            "command": command,
            "context": context,
            "target": target,
            "expression": expression,
        } != LOCKED_JOB_MAP[name]:
            raise SelectionError(
                f"trusted job map entry {name!r} does not match the locked job definition"
            )
        result[name] = {
            "command": command,
            "context": context,
            "target": target,
            "expression": expression,
        }
    if not result:
        raise SelectionError("trusted job map is empty")
    return result


def _is_sha(value: Any) -> bool:
    return isinstance(value, str) and bool(SHA_RE.fullmatch(value))


def _identity_error(selection: Mapping[str, Any], repository: str) -> str | None:
    if selection.get("repository") != repository:
        return "selection repository does not match the configured repository"
    pull = selection.get("pull_number")
    if isinstance(pull, bool) or not isinstance(pull, int) or pull <= 0:
        return "selection pull_number is invalid"
    if not _is_sha(selection.get("base_sha")) or not _is_sha(selection.get("head_sha")):
        return "selection SHA must be 40 lowercase hexadecimal characters"
    return None


def _shape_error(selection: Mapping[str, Any]) -> str | None:
    unexpected = set(selection) - {
        "schema_version",
        "repository",
        "pull_number",
        "base_sha",
        "head_sha",
        "mode",
        "jobs",
        "changed_files",
        "reasons",
    }
    if unexpected:
        return "selection contains unsupported fields"
    if (
        isinstance(selection.get("schema_version"), bool)
        or selection.get("schema_version") != SCHEMA_VERSION
    ):
        return "unsupported selection schema version"
    if selection.get("mode") not in {"selected", "fallback-all"}:
        return "selection mode is invalid"
    for key in ("jobs", "changed_files", "reasons"):
        if not isinstance(selection.get(key), list):
            return f"selection {key} must be an array"
    if any(not isinstance(value, str) for value in selection["jobs"]):
        return "selection jobs must contain strings"
    if any(not isinstance(value, str) for value in selection["changed_files"]):
        return "selection changed_files must contain strings"
    if any(not isinstance(value, str) for value in selection["reasons"]):
        return "selection reasons must contain strings"
    for key in ("changed_files", "reasons"):
        values = selection[key]
        if len(values) > DIAGNOSTIC_MAX_ITEMS:
            return f"selection {key} contains too many items"
        if any(len(value) > DIAGNOSTIC_MAX_LENGTH for value in values):
            return f"selection {key} contains an oversized item"
    return None


def _bounded_diagnostics(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    return [
        item[:DIAGNOSTIC_MAX_LENGTH]
        for item in value[:DIAGNOSTIC_MAX_ITEMS]
        if isinstance(item, str)
    ]


def validate_selection(
    selection: Mapping[str, Any],
    *,
    repository: str = REPOSITORY,
    jobs: Mapping[str, Mapping[str, str]] | None = None,
) -> dict[str, Any]:
    """Validate and normalise a selector artifact.

    Invalid identity is fatal.  A valid identity with malformed selection
    details is conservatively widened to every trusted job, as required by
    the Gatekeeper contract.
    """

    if not isinstance(selection, Mapping):
        raise SelectionError("selection must be a JSON object")
    data = dict(selection)
    error = _identity_error(data, repository)
    if error:
        raise SelectionError(error)
    trusted = jobs or _job_map()
    shape_error = _shape_error(data)
    requested = data.get("jobs", []) if not shape_error else []
    strings_only = isinstance(requested, list) and all(
        isinstance(name, str) for name in requested
    )
    unique = strings_only and len(requested) == len(set(requested))
    allowlisted = strings_only and all(name in trusted for name in requested)
    fallback_complete = (
        data.get("mode") == "fallback-all"
        and strings_only
        and set(requested) == set(trusted)
    )
    if (
        shape_error
        or not unique
        or not allowlisted
        or (data.get("mode") == "fallback-all" and not fallback_complete)
    ):
        data["schema_version"] = SCHEMA_VERSION
        data["mode"] = "fallback-all"
        data["jobs"] = list(trusted)
        reason = "malformed selection widened to all jobs"
        existing_reasons = _bounded_diagnostics(data.get("reasons"))
        data["reasons"] = existing_reasons[: DIAGNOSTIC_MAX_ITEMS - 1] + [
            reason
        ]
    else:
        # Use the trusted file's stable ordering for comment and polling output.
        data["jobs"] = [name for name in trusted if name in requested]
    # Diagnostic arrays are bounded at the artifact boundary.  The evaluator
    # may still use its complete list while making its decision.
    data["changed_files"] = _bounded_diagnostics(data.get("changed_files"))
    data["reasons"] = _bounded_diagnostics(data.get("reasons"))
    data = {
        key: data.get(key)
        for key in (
            "schema_version",
            "repository",
            "pull_number",
            "base_sha",
            "head_sha",
            "mode",
            "jobs",
            "changed_files",
            "reasons",
        )
    }
    return data


def _b64(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def _jwt(app_id: str, private_key: str | bytes | os.PathLike[str], now: float) -> str:
    """Create the short-lived RS256 GitHub App JWT without logging secrets."""

    if hashes is None or serialization is None or padding is None:
        raise DispatchError("cryptography is required to create a GitHub App JWT")
    if isinstance(private_key, bytes):
        key_data = private_key
    elif isinstance(private_key, os.PathLike) or "BEGIN " not in private_key:
        key_data = Path(private_key).read_bytes()
    else:
        key_data = private_key.encode()
    try:
        key = serialization.load_pem_private_key(key_data, password=None)
    except Exception as exc:  # pragma: no cover - key parser details vary.
        raise DispatchError("invalid GitHub App private key") from exc
    issued = int(now) - 60
    payload = {"iat": issued, "exp": issued + 600, "iss": str(app_id)}
    header = {"alg": "RS256", "typ": "JWT"}
    encoded = f"{_b64(json.dumps(header, separators=(',', ':')).encode())}.{_b64(json.dumps(payload, separators=(',', ':')).encode())}"
    signature = key.sign(encoded.encode(), padding.PKCS1v15(), hashes.SHA256())
    return f"{encoded}.{_b64(signature)}"


def _timestamp(value: Any) -> float | None:
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        return float(value)
    if not isinstance(value, str):
        return None
    try:
        parsed = value.replace("Z", "+00:00")
        return _datetime.datetime.fromisoformat(parsed).timestamp()
    except ValueError:
        return None


def _status_timestamp(status: Mapping[str, Any]) -> float | None:
    updated = _timestamp(status.get("updated_at"))
    return updated if updated is not None else _timestamp(status.get("created_at"))


def _response(response: Any) -> tuple[int, Any, Mapping[str, str]]:
    """Normalise common fake HTTP response forms and requests-like responses."""

    if isinstance(response, tuple):
        if len(response) == 2:
            status, body = response
            headers: Mapping[str, str] = {}
        else:
            status, body, headers = response
        return int(status), body, headers or {}
    status = int(getattr(response, "status_code", getattr(response, "status", 200)))
    headers = getattr(response, "headers", {}) or {}
    body: Any
    if hasattr(response, "json"):
        try:
            body = response.json()
        except Exception:
            body = {}
    elif hasattr(response, "read"):
        try:
            body = json.loads(response.read().decode("utf-8"))
        except (OSError, UnicodeDecodeError, ValueError):
            body = {}
    else:
        body = getattr(response, "body", response)
    return status, body, headers


class UrllibHTTP:
    """Small production transport with an injectable urllib opener.

    Tests can pass a local opener or exercise the real urllib path against a
    loopback HTTP server. Authorization values are never placed in errors.
    """

    def __init__(
        self, opener: Callable[..., Any] = urlopen, *, timeout: float = 30.0
    ) -> None:
        self.opener = opener
        self.timeout = timeout

    def request(
        self,
        method: str,
        url: str,
        *,
        headers: Mapping[str, str],
        json: Any = None,
    ) -> Any:
        payload = None if json is None else _json.dumps(json).encode()
        request = Request(
            url,
            data=payload,
            headers={**headers, "Content-Type": "application/json"},
            method=method,
        )
        try:
            try:
                response = self.opener(request, timeout=self.timeout)
            except TypeError:
                response = self.opener(request)
            with response:
                raw_body = response.read()
                try:
                    body = _json.loads(raw_body.decode("utf-8"))
                except (UnicodeDecodeError, ValueError):
                    body = {}
                return int(response.getcode()), body, dict(response.headers or {})
        except HTTPError as exc:
            try:
                body = _json.loads(exc.read().decode("utf-8"))
            except (OSError, UnicodeDecodeError, ValueError):
                body = {}
            return (exc.code, body, dict(exc.headers or {}))
        except (OSError, URLError) as exc:
            raise OSError("GitHub API transport failed") from exc


class GitHubAppClient:
    """Tiny GitHub REST adapter with token renewal and bounded backoff."""

    def __init__(
        self,
        http_client: Any,
        *,
        app_id: str | None = None,
        installation_id: str | None = None,
        private_key: str | None = None,
        clock: Callable[[], float] = time.time,
        sleeper: Callable[[float], None] = time.sleep,
        base_url: str = "https://api.github.com",
        max_retries: int = 3,
    ) -> None:
        self.http = http_client
        self.app_id = app_id
        self.installation_id = installation_id
        self.private_key = private_key
        self._token: str | None = None
        self._token_expires_at = 0.0
        self.clock = clock
        self.sleeper = sleeper
        self.base_url = base_url.rstrip("/")
        self.max_retries = max(1, max_retries)
        self._refreshing = False

    @property
    def token(self) -> str | None:
        return self._token

    def _raw_request(
        self, method: str, url: str, *, headers: Mapping[str, str], payload: Any = None
    ) -> Any:
        client = self.http
        if hasattr(client, "request"):
            try:
                return client.request(method, url, headers=dict(headers), json=payload)
            except TypeError:
                return client.request(method, url, dict(headers), payload)
        if callable(client):
            try:
                return client(method, url, headers=dict(headers), json=payload)
            except TypeError:
                return client(method, url, dict(headers), payload)
        method_fn = getattr(client, method.lower())
        try:
            return method_fn(url, headers=dict(headers), json=payload)
        except TypeError:
            return method_fn(url, dict(headers), payload)

    def _app_token(self, *, force: bool = False) -> str:
        if not force and self._token and self.clock() < self._token_expires_at - 60:
            return self._token
        if not self.app_id or not self.installation_id or not self.private_key:
            if self._token:
                return self._token
            raise DispatchError("GitHub App credentials are incomplete")
        if self._refreshing:
            raise DispatchError("recursive GitHub App token refresh")
        self._refreshing = True
        try:
            jwt = _jwt(self.app_id, self.private_key, self.clock())
            url = f"{self.base_url}/app/installations/{self.installation_id}/access_tokens"
            transient_attempt = 0
            while True:
                response = self._raw_request(
                    "POST",
                    url,
                    headers={
                        "Accept": "application/vnd.github+json",
                        "Authorization": f"Bearer {jwt}",
                    },
                )
                status, body, response_headers = _response(response)
                if status == 429 or status >= 500:
                    transient_attempt += 1
                    if transient_attempt < self.max_retries:
                        self.sleeper(
                            self._retry_delay(response_headers, transient_attempt)
                        )
                        continue
                if status >= 400 or not body.get("token"):
                    raise APIError(
                        f"GitHub App installation token request failed ({status})"
                    )
                self._token = str(body["token"])
                self._token_expires_at = (
                    _timestamp(body.get("expires_at")) or self.clock() + 3600
                )
                return self._token
        finally:
            self._refreshing = False

    def _retry_delay(self, headers: Mapping[str, str], attempt: int) -> float:
        retry_after = headers.get("Retry-After") or headers.get("retry-after")
        try:
            return float(retry_after) if retry_after else 2 ** (attempt - 1)
        except (TypeError, ValueError):
            return 2 ** (attempt - 1)

    def request(self, method: str, path: str, *, payload: Any = None) -> Any:
        """Make an authenticated request, renewing on 401 and backing off transient errors."""

        method = method.upper()
        retryable = method == "GET"
        refreshed = False
        transient_attempt = 0
        while True:
            token = self._app_token(force=refreshed)
            headers = {
                "Accept": "application/vnd.github+json",
                "Authorization": f"token {token}",
            }
            response = self._raw_request(
                method, f"{self.base_url}{path}", headers=headers, payload=payload
            )
            status, body, response_headers = _response(response)
            # A 401 confirms that GitHub rejected the request before applying
            # it, so one token refresh is safe even for comment mutations.
            # Ambiguous transport/5xx failures remain non-retryable for POST
            # and PATCH below.
            if status == 401 and not refreshed:
                self._token = None
                refreshed = True
                continue
            if retryable and (status == 429 or status >= 500):
                transient_attempt += 1
                if transient_attempt < self.max_retries:
                    self.sleeper(self._retry_delay(response_headers, transient_attempt))
                    continue
            if status >= 400:
                raise APIError(f"GitHub API request failed ({status})")
            return body

    def pull(self, repository: str, pull_number: int) -> Mapping[str, Any]:
        return self.request("GET", f"/repos/{repository}/pulls/{pull_number}")

    def statuses(self, repository: str, sha: str) -> list[Mapping[str, Any]]:
        result: list[Mapping[str, Any]] = []
        for page in range(1, MAX_STATUS_PAGES + 1):
            body = self.request(
                "GET",
                f"/repos/{repository}/commits/{sha}/status?per_page={STATUS_PAGE_SIZE}&page={page}",
            )
            if not isinstance(body, Mapping) or not isinstance(
                body.get("statuses"), list
            ):
                raise APIError("GitHub status response is malformed")
            statuses = body["statuses"]
            result.extend(item for item in statuses if isinstance(item, Mapping))
            if len(statuses) < STATUS_PAGE_SIZE:
                return result
        raise APIError("GitHub status pagination exhausted")

    def comments(self, repository: str, pull_number: int) -> list[Mapping[str, Any]]:
        """Return issue comments for safe same-head marker reconciliation."""

        result: list[Mapping[str, Any]] = []
        for page in range(1, MAX_COMMENT_PAGES + 1):
            body = self.request(
                "GET",
                f"/repos/{repository}/issues/{pull_number}/comments"
                f"?per_page={COMMENT_PAGE_SIZE}&page={page}",
            )
            if not isinstance(body, list):
                raise APIError("GitHub comments response is not an array")
            result.extend(item for item in body if isinstance(item, Mapping))
            if len(body) < COMMENT_PAGE_SIZE:
                return result
        raise APIError("GitHub comment pagination exhausted")

    def comment(
        self, repository: str, pull_number: int, body: str
    ) -> Mapping[str, Any]:
        return self.request(
            "POST",
            f"/repos/{repository}/issues/{pull_number}/comments",
            payload={"body": body},
        )

    def patch_comment(
        self, repository: str, comment_id: int, body: str
    ) -> Mapping[str, Any]:
        return self.request(
            "PATCH",
            f"/repos/{repository}/issues/comments/{comment_id}",
            payload={"body": body},
        )


@dataclass
class DispatchResult:
    status: str
    reason: str = ""
    selection: dict[str, Any] = field(default_factory=dict)
    jobs: dict[str, dict[str, Any]] = field(default_factory=dict)
    comment_id: int | None = None
    comment_body: str | None = None
    artifacts: dict[str, str] = field(default_factory=dict)

    @property
    def success(self) -> bool:
        return self.status == "success"

    def to_dict(self) -> dict[str, Any]:
        return {
            "status": self.status,
            "reason": self.reason,
            "selection": self.selection,
            "jobs": self.jobs,
            "comment_id": self.comment_id,
            "artifacts": self.artifacts,
        }

    def __getitem__(self, key: str) -> Any:
        return self.to_dict()[key]


class Dispatcher:
    """Validate, dispatch, and aggregate selected Prow-backed child jobs."""

    def __init__(
        self,
        github: GitHubAppClient | Any | None = None,
        *,
        http_client: Any | None = None,
        app_id: str | None = None,
        installation_id: str | None = None,
        private_key: str | None = None,
        repository: str = REPOSITORY,
        job_file: str | os.PathLike[str] | None = None,
        clock: Callable[[], float] = time.time,
        sleeper: Callable[[float], None] = time.sleep,
        poll_interval: float = 30.0,
        appearance_timeout: float = 5 * 60,
        aggregate_timeout: float = 3 * 60 * 60 + 15 * 60,
        head_check_interval: float = 5 * 60,
    ) -> None:
        if github is None:
            github = http_client
        if github is None:
            raise TypeError("Dispatcher requires a GitHub client or http_client")
        if not isinstance(github, GitHubAppClient) and any(
            value is not None for value in (app_id, installation_id, private_key)
        ):
            github = GitHubAppClient(
                github,
                app_id=app_id,
                installation_id=installation_id,
                private_key=private_key,
                clock=clock,
                sleeper=sleeper,
            )
        self.github = github
        self.repository = repository
        self.jobs = _job_map(job_file)
        self.clock = clock
        self.sleeper = sleeper
        self.poll_interval = poll_interval
        self.appearance_timeout = appearance_timeout
        self.aggregate_timeout = aggregate_timeout
        self.head_check_interval = head_check_interval

    def _api(self, method: str, path: str, *, payload: Any = None) -> Mapping[str, Any]:
        if hasattr(self.github, "request"):
            try:
                return self.github.request(method, path, payload=payload)
            except TypeError:
                try:
                    return self.github.request(method, path, json=payload)
                except TypeError:
                    try:
                        return self.github.request(
                            method, path, headers={}, json=payload
                        )
                    except TypeError:
                        return self.github.request(method, path, payload)
        # A simple fake can expose get/post/patch methods directly.
        fn = getattr(self.github, method.lower(), None)
        if fn is None:
            raise DispatchError("injected GitHub client has no request method")
        return fn(path, json=payload) if payload is not None else fn(path)

    def _pull_head(self, pull_number: int) -> str:
        pull = self._api("GET", f"/repos/{self.repository}/pulls/{pull_number}")
        base = pull.get("base") if isinstance(pull, Mapping) else None
        if not isinstance(base, Mapping) or base.get("ref") != "master":
            raise APIError("GitHub pull request base ref is not master")
        head = pull.get("head") if isinstance(pull, Mapping) else None
        sha = head.get("sha") if isinstance(head, Mapping) else None
        if not _is_sha(sha):
            raise APIError("GitHub pull request response has no valid head SHA")
        return str(sha)

    def _statuses(self, sha: str) -> list[Mapping[str, Any]]:
        result: list[Mapping[str, Any]] = []
        for page in range(1, MAX_STATUS_PAGES + 1):
            body = self._api(
                "GET",
                f"/repos/{self.repository}/commits/{sha}/status?per_page={STATUS_PAGE_SIZE}&page={page}",
            )
            if not isinstance(body, Mapping) or not isinstance(
                body.get("statuses"), list
            ):
                raise APIError("GitHub status response is malformed")
            values = body["statuses"]
            result.extend(value for value in values if isinstance(value, Mapping))
            if len(values) < STATUS_PAGE_SIZE:
                return result
        raise APIError("GitHub status pagination exhausted")

    def _classify(
        self, statuses: Iterable[Mapping[str, Any]], selection: Mapping[str, Any]
    ) -> dict[str, dict[str, Any]]:
        by_context: dict[str, Mapping[str, Any]] = {}
        wanted = {self.jobs[name]["context"]: name for name in selection["jobs"]}
        for item in statuses:
            context = item.get("context")
            target = item.get("target_url")
            if (
                context not in wanted
                or not isinstance(target, str)
                or not target.startswith(PROW_URL_PREFIX)
            ):
                continue
            # The combined-status API may return duplicate contexts.  GitHub's
            # response is newest-first, but timestamps make the rule explicit.
            previous = by_context.get(str(context))
            candidate_time = _status_timestamp(item)
            previous_time = (
                _status_timestamp(previous) if previous is not None else None
            )
            if previous is None or (
                candidate_time is not None
                and previous_time is not None
                and candidate_time > previous_time
            ):
                by_context[str(context)] = item
        result: dict[str, dict[str, Any]] = {}
        for name in selection["jobs"]:
            context = self.jobs[name]["context"]
            item = by_context.get(context)
            result[name] = {
                "context": context,
                "target_url": item.get("target_url") if item else None,
                "state": str(item.get("state", "missing")).lower()
                if item
                else "missing",
                "created_at": item.get("created_at") if item else None,
                "updated_at": item.get("updated_at") if item else None,
                "status": dict(item) if item else None,
            }
        return result

    def _comment_body(
        self,
        head: str,
        names: Sequence[str],
        *,
        result: str | None = None,
        jobs: Mapping[str, Any] | None = None,
    ) -> str:
        lines = [COMMENT_MARKER.format(head=head)]
        lines.extend(self.jobs[name]["command"] for name in names)
        if result:
            lines.append("")
            lines.append(f"kserve-test-selector: {result}")
            if jobs:
                for name in names:
                    state = jobs.get(name, {}).get("state", "missing")
                    lines.append(f"- {name}: {state}")
        return "\n".join(lines)

    def _existing_marker_comment(
        self,
        pull_number: int,
        head: str,
        names: Sequence[str],
    ) -> tuple[int, str, float] | None:
        """Find this App's unfinished command comment for the current head.

        A prior POST may have succeeded while its response was lost.  The
        installation token can read issue comments, so a comment authored by
        the fixed App bot login is a safe idempotency record.  Reconcile only
        an exact command set; partial or completed comments are left alone and
        the normal one-shot POST path is used.
        """

        comments_fn = getattr(self.github, "comments", None)
        if not callable(comments_fn):
            return None
        comments = comments_fn(self.repository, pull_number)
        if not isinstance(comments, list):
            return None
        expected_marker = COMMENT_MARKER.format(head=head)
        expected_commands = {self.jobs[name]["command"]: name for name in names}
        candidates: list[tuple[float, int, str]] = []
        for comment in comments:
            if not isinstance(comment, Mapping):
                continue
            author = comment.get("user")
            if not isinstance(author, Mapping) or author.get("login") != APP_BOT_LOGIN:
                continue
            body = comment.get("body")
            comment_id = comment.get("id")
            created_at = _timestamp(comment.get("created_at"))
            if not isinstance(body, str) or expected_marker not in body.splitlines():
                continue
            if "kserve-test-selector:" in body:
                continue
            found_commands = {
                line.strip()
                for line in body.splitlines()
                if line.strip() in expected_commands
            }
            if found_commands != set(expected_commands):
                continue
            if (
                isinstance(comment_id, bool)
                or not isinstance(comment_id, int)
                or comment_id <= 0
            ):
                continue
            candidates.append(
                (
                    created_at if created_at is not None else float("-inf"),
                    comment_id,
                    body,
                )
            )
        if not candidates:
            return None
        created_at, comment_id, body = max(
            candidates, key=lambda candidate: (candidate[0], candidate[1])
        )
        return (
            comment_id,
            body,
            created_at if created_at != float("-inf") else self.clock(),
        )

    def run(
        self,
        selection: Mapping[str, Any],
        *,
        artifact_dir: str | os.PathLike[str] | None = None,
    ) -> DispatchResult:
        try:
            selection_data = validate_selection(
                selection, repository=self.repository, jobs=self.jobs
            )
        except SelectionError as exc:
            result = DispatchResult(
                "failure",
                str(exc),
                dict(selection) if isinstance(selection, Mapping) else {},
            )
            self._write_artifacts(result, artifact_dir)
            return result

        pull_number = selection_data["pull_number"]
        expected_head = selection_data["head_sha"]
        try:
            if self._pull_head(pull_number) != expected_head:
                result = DispatchResult(
                    "failure",
                    "pull request head changed before dispatch",
                    selection_data,
                )
                self._write_artifacts(result, artifact_dir)
                return result
            if not selection_data["jobs"]:
                result = DispatchResult("success", "no selected jobs", selection_data)
                self._write_artifacts(result, artifact_dir)
                return result

            initial = self._classify(self._statuses(expected_head), selection_data)
            need_dispatch = [
                name
                for name in selection_data["jobs"]
                if initial[name]["state"] in {"missing", *UNSUCCESSFUL_STATES}
            ]
            comment_id: int | None = None
            comment_body: str | None = None
            command_time = self.clock()
            dispatched = list(need_dispatch)
            comment_names: Sequence[str] = ()
            if need_dispatch:
                existing = self._existing_marker_comment(
                    pull_number, expected_head, need_dispatch
                )
                if existing is not None:
                    comment_id, comment_body, command_time = existing
                    comment_names = tuple(need_dispatch)
                else:
                    comment_body = self._comment_body(expected_head, need_dispatch)
                    comment = self._api(
                        "POST",
                        f"/repos/{self.repository}/issues/{pull_number}/comments",
                        payload={"body": comment_body},
                    )
                    if not isinstance(comment, Mapping) or comment.get("id") is None:
                        raise APIError("GitHub comment response has no comment id")
                    comment_id = int(comment["id"])
                    command_time = (
                        _timestamp(comment.get("created_at"))
                        if isinstance(comment, Mapping)
                        else None
                    )
                    command_time = (
                        command_time if command_time is not None else self.clock()
                    )
                    comment_names = tuple(need_dispatch)

            result = self._wait(
                selection_data,
                initial,
                dispatched,
                command_time=command_time,
                comment_id=comment_id,
                comment_body=comment_body,
                comment_names=comment_names,
            )
            self._write_artifacts(result, artifact_dir)
            return result
        except DispatchError as exc:
            result = DispatchResult("failure", str(exc), selection_data)
            self._write_artifacts(result, artifact_dir)
            return result
        except (KeyError, TypeError, ValueError) as exc:
            result = DispatchResult(
                "failure", f"malformed GitHub response: {exc}", selection_data
            )
            self._write_artifacts(result, artifact_dir)
            return result
        except OSError as exc:
            result = DispatchResult(
                "failure", f"GitHub transport failed: {exc}", selection_data
            )
            self._write_artifacts(result, artifact_dir)
            return result

    def _wait(
        self,
        selection: Mapping[str, Any],
        initial: dict[str, dict[str, Any]],
        dispatched: Sequence[str],
        *,
        command_time: float,
        comment_id: int | None,
        comment_body: str | None,
        comment_names: Sequence[str] = (),
    ) -> DispatchResult:
        started = self.clock()
        logical_elapsed = 0.0
        next_head_check = self.head_check_interval
        current = initial
        reason = "all selected jobs passed"
        while True:
            elapsed = max(logical_elapsed, self.clock() - started)
            if elapsed >= next_head_check:
                if self._pull_head(selection["pull_number"]) != selection["head_sha"]:
                    reason = "pull request head changed while waiting"
                    return self._finish(
                        selection,
                        current,
                        "failure",
                        reason,
                        comment_id,
                        comment_body,
                        comment_names or dispatched,
                    )
                next_head_check += self.head_check_interval

            # A status created before our command cannot satisfy a dispatched
            # job.  Existing pending statuses intentionally remain reusable.
            statuses = self._classify(self._statuses(selection["head_sha"]), selection)
            for name in selection["jobs"]:
                observed = statuses[name]
                if name in dispatched:
                    observed_time = _timestamp(
                        observed.get("updated_at")
                    ) or _timestamp(observed.get("created_at"))
                    if (
                        observed["state"] in TERMINAL_STATES
                        and observed_time is not None
                        and observed_time < command_time
                    ):
                        observed = dict(observed)
                        observed["state"] = (
                            "pending"
                            if elapsed < self.appearance_timeout
                            else "missing"
                        )
                current[name] = observed

            failures = [
                name
                for name in selection["jobs"]
                if current[name]["state"] in UNSUCCESSFUL_STATES
            ]
            if failures:
                reason = "child job failed: " + ", ".join(failures)
                return self._finish(
                    selection,
                    current,
                    "failure",
                    reason,
                    comment_id,
                    comment_body,
                    comment_names or dispatched,
                )
            if all(current[name]["state"] == "success" for name in selection["jobs"]):
                if self._pull_head(selection["pull_number"]) != selection["head_sha"]:
                    reason = "pull request head changed before aggregate success"
                    return self._finish(
                        selection,
                        current,
                        "failure",
                        reason,
                        comment_id,
                        comment_body,
                        comment_names or dispatched,
                    )
                return self._finish(
                    selection,
                    current,
                    "success",
                    reason,
                    comment_id,
                    comment_body,
                    comment_names or dispatched,
                )

            absent = [
                name for name in dispatched if current[name]["state"] == "missing"
            ]
            if absent and elapsed >= self.appearance_timeout:
                reason = "child status did not appear: " + ", ".join(absent)
                return self._finish(
                    selection,
                    current,
                    "failure",
                    reason,
                    comment_id,
                    comment_body,
                    comment_names or dispatched,
                )
            if elapsed >= self.aggregate_timeout:
                reason = "aggregate timeout"
                return self._finish(
                    selection,
                    current,
                    "failure",
                    reason,
                    comment_id,
                    comment_body,
                    comment_names or dispatched,
                )
            self.sleeper(self.poll_interval)
            # A zero-duration sleeper is useful in tests, but must not turn a
            # missing status into an infinite loop when the injected clock is
            # also constant.  One logical second per iteration preserves the
            # normal path (30 seconds) and keeps timeout behavior finite.
            logical_elapsed += self.poll_interval if self.poll_interval > 0 else 1.0

    def _finish(
        self,
        selection: Mapping[str, Any],
        jobs: dict[str, dict[str, Any]],
        status: str,
        reason: str,
        comment_id: int | None,
        original_body: str | None,
        comment_names: Sequence[str],
    ) -> DispatchResult:
        final_body = original_body
        if comment_id is not None:
            final_body = self._comment_body(
                selection["head_sha"], comment_names, result=status, jobs=jobs
            )
            self._api(
                "PATCH",
                f"/repos/{self.repository}/issues/comments/{comment_id}",
                payload={"body": final_body},
            )
        return DispatchResult(
            status, reason, dict(selection), jobs, comment_id, final_body
        )

    def _write_artifacts(
        self, result: DispatchResult, artifact_dir: str | os.PathLike[str] | None
    ) -> None:
        if not artifact_dir:
            return
        destination = Path(artifact_dir)
        destination.mkdir(parents=True, exist_ok=True)
        aggregation_path = destination / "aggregation.json"
        junit_path = destination / "junit.xml"
        result.artifacts = {
            "aggregation": str(aggregation_path),
            "junit": str(junit_path),
        }
        aggregation = result.to_dict()
        aggregation["jobs"] = result.jobs
        aggregation["comment_body"] = result.comment_body
        aggregation["artifacts"] = result.artifacts
        aggregation_path.write_text(
            json.dumps(aggregation, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )

        failed_jobs = [
            name
            for name, job in result.jobs.items()
            if job.get("state") in {"missing", *UNSUCCESSFUL_STATES}
        ]
        aggregate_failure = not result.success and not failed_jobs
        suite = ET.Element(
            "testsuite",
            name="kserve-test-selector",
            tests=str(
                max(1, len(result.jobs)) if aggregate_failure else len(result.jobs)
            ),
            failures=str(
                len(failed_jobs) if failed_jobs else (1 if aggregate_failure else 0)
            ),
        )
        for name, job in result.jobs.items():
            case = ET.SubElement(suite, "testcase", name=name)
            if job.get("state") in {"missing", *UNSUCCESSFUL_STATES}:
                failure = ET.SubElement(case, "failure", message=str(job.get("state")))
                failure.text = result.reason
        if aggregate_failure:
            if result.jobs:
                case = suite.find("testcase")
            else:
                case = ET.SubElement(suite, "testcase", name="gatekeeper")
            failure = ET.SubElement(case, "failure", message=result.status)
            failure.text = result.reason
        ET.ElementTree(suite).write(junit_path, encoding="utf-8", xml_declaration=True)


def dispatch(
    selection: Mapping[str, Any],
    http_client: Any,
    *,
    app_id: str | None = None,
    installation_id: str | None = None,
    private_key: str | None = None,
    repository: str = REPOSITORY,
    artifact_dir: str | os.PathLike[str] | None = None,
    clock: Callable[[], float] = time.time,
    sleeper: Callable[[float], None] = time.sleep,
    **dispatcher_options: Any,
) -> DispatchResult:
    """Convenience public entry point for embedding the Gatekeeper.

    ``http_client`` may already be a :class:`GitHubAppClient` (useful for a
    test fake), or any requests-like injected transport accepted by that
    adapter.
    """

    github = (
        http_client
        if isinstance(http_client, GitHubAppClient)
        else GitHubAppClient(
            http_client,
            app_id=app_id,
            installation_id=installation_id,
            private_key=private_key,
            clock=clock,
            sleeper=sleeper,
        )
    )
    return Dispatcher(
        github,
        repository=repository,
        clock=clock,
        sleeper=sleeper,
        **dispatcher_options,
    ).run(selection, artifact_dir=artifact_dir)


# Names used by small embedding scripts and older POC examples.
normalize_selection = validate_selection
create_app_jwt = _jwt


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Dispatch and aggregate KServe ODH Prow E2Es"
    )
    parser.add_argument("--selection", required=True, help="trusted selection.json")
    parser.add_argument("--app-id", default=os.getenv("GITHUB_APP_ID"))
    parser.add_argument(
        "--installation-id", default=os.getenv("GITHUB_INSTALLATION_ID")
    )
    parser.add_argument("--private-key", default=os.getenv("GITHUB_APP_PRIVATE_KEY"))
    parser.add_argument("--artifact-dir", default=os.getenv("ARTIFACT_DIR"))
    parser.add_argument("--poll-interval", type=float, default=30.0)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        selection = json.loads(Path(args.selection).read_text(encoding="utf-8"))
        github = GitHubAppClient(
            UrllibHTTP(),
            app_id=args.app_id,
            installation_id=args.installation_id,
            private_key=args.private_key,
        )
        result = Dispatcher(
            github, repository=REPOSITORY, poll_interval=args.poll_interval
        ).run(selection, artifact_dir=args.artifact_dir)
        print(json.dumps(result.to_dict(), sort_keys=True))
        return 0 if result.success else 1
    except (OSError, ValueError, DispatchError) as exc:
        print(f"gatekeeper dispatch failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
