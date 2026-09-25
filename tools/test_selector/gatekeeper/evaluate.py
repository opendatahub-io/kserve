"""Evaluate a pull request in a trusted merged worktree.

The evaluator is deliberately small at its public boundary: validate the
Prow identity, build a merged checkout, ask the packaged selector for a job
decision, and write one validated artifact.  Git and selector execution are
injected so tests never need network access or credentials.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping, Protocol, Sequence


REPOSITORY = "opendatahub-io/kserve"
BASE_REF = "master"
SHA_RE = re.compile(r"^[0-9a-f]{40}$")

# These are the only jobs a selector result may name.  Keep this list in
# trusted code; it must never come from the pull request worktree.
ALLOWLISTED_JOBS = (
    "e2e-graph",
    "e2e-raw",
    "e2e-predictor",
    "e2e-llm-inference-service",
)
JOB_TARGETS = {
    "e2e-graph": "e2e-graph-ocp",
    "e2e-raw": "e2e-raw-ocp",
    "e2e-predictor": "e2e-predictor-ocp",
    "e2e-llm-inference-service": "e2e-llmisvc-ocp",
}
CLONE_URL = "https://github.com/opendatahub-io/kserve.git"
MAX_CHANGED_FILES = 200
MAX_REASONS = 200
MAX_REASON_LENGTH = 500
DEFAULT_CONFIG = Path(__file__).resolve().parents[1] / "config.json"
DEFAULT_JOB_MAP = Path(__file__).with_name("ocp_jobs.json")


class EvaluationError(RuntimeError):
    """Base class for evaluator failures which must fail the presubmit."""


class InvalidJobSpec(EvaluationError):
    """The Prow job identity is missing, ambiguous, or not trusted."""


class StaleJobIdentity(InvalidJobSpec):
    """The fetched pull ref is not the SHA authenticated by ``JOB_SPEC``."""


class MergeConflict(EvaluationError):
    """The pull request cannot be represented as a merged worktree."""


class CommandFailure(EvaluationError):
    """A trusted command returned a non-zero status."""


@dataclass(frozen=True)
class JobIdentity:
    repository: str
    pull_number: int
    base_sha: str
    head_sha: str


@dataclass(frozen=True)
class CommandResult:
    returncode: int
    stdout: str = ""
    stderr: str = ""


class CommandRunner(Protocol):
    def run(
        self,
        command: Sequence[str],
        *,
        cwd: Path | None = None,
        env: Mapping[str, str] | None = None,
    ) -> CommandResult:
        """Run one command and return captured text output."""


class SubprocessRunner:
    """Production command runner; tests inject a deterministic replacement."""

    def run(
        self,
        command: Sequence[str],
        *,
        cwd: Path | None = None,
        env: Mapping[str, str] | None = None,
    ) -> CommandResult:
        completed = subprocess.run(
            list(command),
            cwd=cwd,
            env=dict(env) if env is not None else None,
            text=True,
            capture_output=True,
            check=False,
        )
        return CommandResult(completed.returncode, completed.stdout, completed.stderr)


@dataclass(frozen=True)
class SelectionArtifact:
    repository: str
    pull_number: int
    base_sha: str
    head_sha: str
    mode: str
    jobs: tuple[str, ...]
    changed_files: tuple[str, ...]
    reasons: tuple[str, ...]

    def to_dict(self) -> dict[str, Any]:
        return {
            "schema_version": 1,
            "repository": self.repository,
            "pull_number": self.pull_number,
            "base_sha": self.base_sha,
            "head_sha": self.head_sha,
            "mode": self.mode,
            "jobs": list(self.jobs),
            "changed_files": list(self.changed_files),
            "reasons": list(self.reasons),
        }


def parse_job_spec(job_spec: Mapping[str, Any] | str) -> JobIdentity:
    """Validate and normalize the small trusted identity portion of JOB_SPEC."""

    if isinstance(job_spec, str):
        try:
            job_spec = json.loads(job_spec)
        except (TypeError, ValueError) as exc:
            raise InvalidJobSpec("JOB_SPEC is not valid JSON") from exc
    if not isinstance(job_spec, Mapping):
        raise InvalidJobSpec("JOB_SPEC must be a JSON object")
    if job_spec.get("type") != "presubmit":
        raise InvalidJobSpec("JOB_SPEC type must be presubmit")

    refs = job_spec.get("refs")
    if not isinstance(refs, Mapping):
        raise InvalidJobSpec("JOB_SPEC.refs must be an object")
    if refs.get("org") != "opendatahub-io" or refs.get("repo") != "kserve":
        raise InvalidJobSpec("JOB_SPEC repository is not opendatahub-io/kserve")
    if refs.get("base_ref") != BASE_REF:
        raise InvalidJobSpec("JOB_SPEC base ref is not master")

    base_sha = refs.get("base_sha")
    if not isinstance(base_sha, str) or not SHA_RE.fullmatch(base_sha):
        raise InvalidJobSpec("JOB_SPEC base_sha must be a lowercase 40-hex SHA")

    pulls = refs.get("pulls")
    if not isinstance(pulls, list) or len(pulls) != 1:
        raise InvalidJobSpec("JOB_SPEC must contain exactly one pull")
    pull = pulls[0]
    if not isinstance(pull, Mapping):
        raise InvalidJobSpec("JOB_SPEC pull must be an object")
    pull_number = pull.get("number")
    if (
        isinstance(pull_number, bool)
        or not isinstance(pull_number, int)
        or pull_number <= 0
    ):
        raise InvalidJobSpec("JOB_SPEC pull number must be a positive integer")
    head_sha = pull.get("sha")
    if not isinstance(head_sha, str) or not SHA_RE.fullmatch(head_sha):
        raise InvalidJobSpec("JOB_SPEC pull sha must be a lowercase 40-hex SHA")

    return JobIdentity(REPOSITORY, pull_number, base_sha, head_sha)


def evaluate(
    job_spec: Mapping[str, Any] | str,
    *,
    shared_dir: Path,
    artifact_dir: Path,
    runner: CommandRunner | Any | None = None,
    selector_runner: CommandRunner | Any | None = None,
    work_dir: Path | None = None,
    config_path: Path | None = None,
    job_map_path: Path | None = None,
) -> SelectionArtifact:
    """Evaluate ``JOB_SPEC`` and atomically write the selection artifact.

    A valid identity with an internal clone, diff, or selector error widens to
    all jobs. Invalid identity and merge conflicts raise without writing an
    artifact, which prevents a dispatcher from acting on an unsafe result.
    """

    identity = parse_job_spec(job_spec)
    command_runner = runner if runner is not None else SubprocessRunner()
    selector_command_runner = (
        selector_runner if selector_runner is not None else command_runner
    )
    config = Path(config_path) if config_path is not None else DEFAULT_CONFIG

    owns_work_dir = work_dir is None
    temporary_root: tempfile.TemporaryDirectory[str] | None = None
    if owns_work_dir:
        temporary_root = tempfile.TemporaryDirectory(prefix="kserve-gatekeeper-")
        root = Path(temporary_root.name)
    else:
        root = Path(work_dir)
        root.mkdir(parents=True, exist_ok=True)
    repo_dir = root / "repo"
    changed_files: list[str] = []

    try:
        try:
            _clone_and_fetch(command_runner, identity, root, repo_dir)
            changed_files = _changed_files(command_runner, identity, repo_dir)
            _checkout_and_merge(command_runner, identity, repo_dir)
            jobs, reasons = _run_selector(
                selector_command_runner,
                repo_dir,
                root,
                changed_files,
                config,
                job_map_path,
            )
            artifact = _artifact(
                identity,
                mode="selected",
                jobs=jobs,
                changed_files=changed_files,
                reasons=reasons,
            )
        except MergeConflict:
            raise
        except InvalidJobSpec:
            # A fetched ref mismatch is an identity failure, not selector
            # uncertainty. Never emit fallback-all for an unauthenticated PR.
            raise
        except Exception as exc:  # valid identity: uncertainty widens safely
            artifact = _artifact(
                identity,
                mode="fallback-all",
                jobs=ALLOWLISTED_JOBS,
                changed_files=changed_files,
                reasons=(f"fallback-all: {type(exc).__name__}: {exc}",),
            )
        _write_artifacts(artifact, Path(shared_dir), Path(artifact_dir))
        return artifact
    finally:
        if temporary_root is not None:
            temporary_root.cleanup()


def _clone_and_fetch(
    runner: CommandRunner | Any,
    identity: JobIdentity,
    root: Path,
    repo_dir: Path,
) -> None:
    _run_checked(
        runner,
        [
            "git",
            "clone",
            "--filter=blob:none",
            "--no-checkout",
            CLONE_URL,
            str(repo_dir),
        ],
        cwd=root,
        env=_git_environment(),
    )
    _run_checked(
        runner,
        ["git", "fetch", "--no-tags", "origin", identity.base_sha],
        cwd=repo_dir,
        env=_git_environment(),
    )
    pull_ref = f"refs/pull/{identity.pull_number}/head"
    local_pull_ref = f"refs/remotes/origin/pull/{identity.pull_number}/head"
    _run_checked(
        runner,
        ["git", "fetch", "--no-tags", "origin", f"+{pull_ref}:{local_pull_ref}"],
        cwd=repo_dir,
        env=_git_environment(),
    )
    fetched_head = _run_checked(
        runner,
        ["git", "rev-parse", local_pull_ref],
        cwd=repo_dir,
        env=_git_environment(),
    ).stdout.strip()
    if fetched_head != identity.head_sha:
        raise StaleJobIdentity("fetched pull ref does not match JOB_SPEC head_sha")


def _checkout_and_merge(
    runner: CommandRunner | Any,
    identity: JobIdentity,
    repo_dir: Path,
) -> None:
    _run_checked(
        runner,
        ["git", "checkout", "--detach", identity.base_sha],
        cwd=repo_dir,
        env=_git_environment(),
    )
    merge = _run(
        runner,
        ["git", "merge", "--no-commit", "--no-ff", identity.head_sha],
        cwd=repo_dir,
        env=_git_environment(),
    )
    if merge.returncode != 0:
        # Best effort cleanup; the important contract is that no selection is
        # emitted when the Prow merged-worktree model cannot be constructed.
        _run(runner, ["git", "merge", "--abort"], cwd=repo_dir, env=_git_environment())
        raise MergeConflict(merge.stderr.strip() or "pull request merge failed")


def _changed_files(
    runner: CommandRunner | Any, identity: JobIdentity, repo_dir: Path
) -> list[str]:
    result = _run_checked(
        runner,
        [
            "git",
            "diff",
            "--name-status",
            "--find-renames",
            f"{identity.base_sha}...{identity.head_sha}",
        ],
        cwd=repo_dir,
        env=_git_environment(),
    )
    paths: list[str] = []
    for line in result.stdout.splitlines():
        fields = line.split("\t")
        if not re.fullmatch(r"(?:[AMD]|[RC][0-9]*)", fields[0]):
            raise CommandFailure("git diff emitted malformed name-status output")
        expected_fields = 3 if fields[0][0] in "RC" else 2
        if len(fields) != expected_fields or any(
            not path.strip() for path in fields[1:]
        ):
            record_kind = "rename/copy" if fields[0][0] in "RC" else "file"
            raise CommandFailure(f"git diff emitted an incomplete {record_kind} record")
        paths.extend(fields[1:])
    return _unique_nonempty(paths)


def _run_selector(
    runner: CommandRunner | Any,
    repo_dir: Path,
    root: Path,
    changed_files: list[str],
    config_path: Path,
    job_map_path: Path | None,
) -> tuple[tuple[str, ...], tuple[str, ...]]:
    changed_path = root / "changed-files.txt"
    changed_path.write_text(
        "".join(f"{path}\n" for path in changed_files), encoding="utf-8"
    )
    mapping_path = root / "mapping.json"
    trusted_job_map = (
        Path(job_map_path) if job_map_path is not None else DEFAULT_JOB_MAP
    )
    _validate_trusted_job_map(trusted_job_map)

    prefix = [sys.executable, "-m", "test_selector", "--config", str(config_path)]
    learn = _run_checked(
        runner,
        [
            *prefix,
            "learn",
            "--repo",
            str(repo_dir),
            "--output",
            str(mapping_path),
        ],
        cwd=root,
        env=_selector_environment(),
    )
    del learn
    query = _run_checked(
        runner,
        [
            *prefix,
            "query",
            "--repo",
            str(repo_dir),
            "--mapping",
            str(mapping_path),
            "--changed-files-file",
            str(changed_path),
            "--match-jobs-file",
            str(trusted_job_map),
        ],
        cwd=root,
        env=_selector_environment(),
    )
    return _parse_selector_output(query.stdout)


def _validate_trusted_job_map(path: Path) -> None:
    """Validate the structured trusted map before passing it to the selector."""

    if path.is_symlink():
        raise CommandFailure(f"refusing symlink trusted job map: {path}")
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise CommandFailure(f"trusted job map is unavailable: {path}") from exc
    if not isinstance(raw, Mapping) or set(raw) != set(ALLOWLISTED_JOBS):
        raise CommandFailure(
            "trusted job map does not contain exactly the allowlisted jobs"
        )
    for name in ALLOWLISTED_JOBS:
        record = raw[name]
        if not isinstance(record, Mapping) or set(record) != {
            "command",
            "context",
            "target",
            "expression",
        }:
            raise CommandFailure(f"trusted job map entry {name!r} is invalid")
        if (
            record["command"] != f"/test {name}"
            or record["context"] != f"ci/prow/{name}"
        ):
            raise CommandFailure(
                f"trusted job map entry {name!r} has invalid Prow metadata"
            )
        if record["target"] != JOB_TARGETS[name] or not isinstance(
            record["expression"], str
        ):
            raise CommandFailure(
                f"trusted job map entry {name!r} has invalid target or expression"
            )


def _parse_selector_output(stdout: str) -> tuple[tuple[str, ...], tuple[str, ...]]:
    """Parse the intentionally narrow selector match output."""

    jobs: dict[str, bool] = {}
    reasons: list[str] = []
    for raw_line in stdout.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        if line.startswith("{"):
            try:
                decoded = json.loads(line)
            except ValueError as exc:
                raise CommandFailure("selector emitted malformed JSON") from exc
            if not isinstance(decoded, Mapping):
                raise CommandFailure("selector output must be an object")
            output_jobs = decoded.get("jobs")
            if isinstance(output_jobs, list) and all(
                isinstance(job, str) for job in output_jobs
            ):
                for job in output_jobs:
                    if job not in ALLOWLISTED_JOBS:
                        raise CommandFailure("selector emitted an unknown job")
                    if job in jobs:
                        raise CommandFailure("selector emitted duplicate job result")
                    jobs[job] = True
            output_reasons = decoded.get("reasons")
            if isinstance(output_reasons, list):
                reasons.extend(str(reason) for reason in output_reasons)
            continue
        if "=" not in line:
            # Selector diagnostics are not dispatch input. Keep them only as
            # capped reasons after a recognized result has been found.
            if jobs:
                reasons.append(line)
            continue
        name, value = (part.strip() for part in line.split("=", 1))
        if name not in ALLOWLISTED_JOBS or value not in {"true", "false"}:
            raise CommandFailure("selector emitted an unknown or invalid job result")
        if name in jobs:
            raise CommandFailure("selector emitted duplicate job result")
        jobs[name] = value == "true"

    if not jobs:
        raise CommandFailure("selector emitted no job results")
    missing = [job for job in ALLOWLISTED_JOBS if job not in jobs]
    if missing:
        raise CommandFailure(
            "selector emitted incomplete job results: " + ", ".join(missing)
        )
    selected = tuple(job for job in ALLOWLISTED_JOBS if jobs.get(job, False))
    return selected, tuple(reasons)


def _artifact(
    identity: JobIdentity,
    *,
    mode: str,
    jobs: Sequence[str],
    changed_files: Sequence[str],
    reasons: Sequence[str],
) -> SelectionArtifact:
    normalized_jobs = tuple(job for job in ALLOWLISTED_JOBS if job in set(jobs))
    if mode == "fallback-all":
        normalized_jobs = ALLOWLISTED_JOBS
    return SelectionArtifact(
        repository=identity.repository,
        pull_number=identity.pull_number,
        base_sha=identity.base_sha,
        head_sha=identity.head_sha,
        mode=mode,
        jobs=normalized_jobs,
        changed_files=tuple(_cap_diagnostics(changed_files, MAX_CHANGED_FILES)),
        reasons=tuple(_cap_diagnostics(reasons, MAX_REASONS)),
    )


def _cap_diagnostics(values: Sequence[Any], limit: int) -> list[str]:
    if len(values) <= limit:
        return [str(value)[:MAX_REASON_LENGTH] for value in values]
    if limit == 1:
        return [f"diagnostics truncated after {limit} entries"]
    return [str(value)[:MAX_REASON_LENGTH] for value in values[: limit - 1]] + [
        f"diagnostics truncated after {limit - 1} entries"
    ]


def _write_artifacts(
    artifact: SelectionArtifact, shared_dir: Path, artifact_dir: Path
) -> None:
    payload = json.dumps(artifact.to_dict(), indent=2, sort_keys=True) + "\n"
    for directory in (shared_dir, artifact_dir):
        directory.mkdir(parents=True, exist_ok=True)
        _atomic_write(directory / "selection.json", payload)


def _atomic_write(path: Path, payload: str) -> None:
    fd, temporary_name = tempfile.mkstemp(
        prefix=f".{path.name}.", suffix=".tmp", dir=path.parent
    )
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary_name, path)
    finally:
        if os.path.exists(temporary_name):
            os.unlink(temporary_name)


def _run(
    runner: CommandRunner | Any,
    command: Sequence[str],
    *,
    cwd: Path | None = None,
    env: Mapping[str, str] | None = None,
) -> CommandResult:
    target = getattr(runner, "run", None)
    if target is None:
        target = runner
    try:
        result = target(command, cwd=cwd, env=env)
    except TypeError:
        try:
            result = target(command, cwd=cwd)
        except TypeError:
            # The smallest useful test double can accept just argv.
            result = target(command)
    if isinstance(result, CommandResult):
        return result
    return CommandResult(
        int(
            getattr(result, "returncode", result[0] if isinstance(result, tuple) else 1)
        ),
        str(
            getattr(
                result,
                "stdout",
                result[1] if isinstance(result, tuple) and len(result) > 1 else "",
            )
            or ""
        ),
        str(
            getattr(
                result,
                "stderr",
                result[2] if isinstance(result, tuple) and len(result) > 2 else "",
            )
            or ""
        ),
    )


def _run_checked(
    runner: CommandRunner | Any,
    command: Sequence[str],
    *,
    cwd: Path | None = None,
    env: Mapping[str, str] | None = None,
) -> CommandResult:
    result = _run(runner, command, cwd=cwd, env=env)
    if result.returncode != 0:
        raise CommandFailure(
            result.stderr.strip() or f"command failed: {' '.join(command)}"
        )
    return result


def _git_environment() -> dict[str, str]:
    safe_keys = {
        "PATH",
        "HOME",
        "LANG",
        "LC_ALL",
        "SSL_CERT_FILE",
        "SSL_CERT_DIR",
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "NO_PROXY",
        "http_proxy",
        "https_proxy",
        "no_proxy",
    }
    env = {key: value for key, value in os.environ.items() if key in safe_keys}
    env["GIT_TERMINAL_PROMPT"] = "0"
    env["GIT_CONFIG_NOSYSTEM"] = "1"
    env["GIT_CONFIG_GLOBAL"] = os.devnull
    # ``git merge --no-commit`` still needs an identity to construct the
    # index/tree used by the selector. This is not a signed or pushed commit.
    env.setdefault("GIT_AUTHOR_NAME", "KServe Gatekeeper")
    env.setdefault("GIT_AUTHOR_EMAIL", "kserve-gatekeeper@example.invalid")
    env.setdefault("GIT_COMMITTER_NAME", "KServe Gatekeeper")
    env.setdefault("GIT_COMMITTER_EMAIL", "kserve-gatekeeper@example.invalid")
    return env


def _selector_environment() -> dict[str, str]:
    env = _git_environment()
    # The selector is trusted image code. Never inherit a PR-controlled or
    # caller-controlled import path into a subprocess whose ``--repo`` points
    # at the merged worktree; a top-level ``test_selector`` there must not
    # shadow this package.
    env["PYTHONPATH"] = str(Path(__file__).resolve().parents[2])
    env["GOCACHE"] = "/tmp/go-build"
    env["GOMODCACHE"] = "/tmp/go-mod"
    env["GOFLAGS"] = "-buildvcs=false"
    env["GOTOOLCHAIN"] = "local"
    return env


def _unique_nonempty(values: Sequence[str]) -> list[str]:
    seen: set[str] = set()
    result: list[str] = []
    for value in values:
        normalized = value.strip().removeprefix("./")
        if normalized and normalized not in seen:
            seen.add(normalized)
            result.append(normalized)
    return result


def main(argv: Sequence[str] | None = None) -> int:
    import argparse

    parser = argparse.ArgumentParser(
        description="Evaluate KServe's trusted test selection"
    )
    parser.add_argument("--job-spec", default=os.environ.get("JOB_SPEC"))
    parser.add_argument("--shared-dir", default=os.environ.get("SHARED_DIR"))
    parser.add_argument("--artifact-dir", default=os.environ.get("ARTIFACT_DIR"))
    args = parser.parse_args(argv)
    if not args.job_spec or not args.shared_dir or not args.artifact_dir:
        parser.error("JOB_SPEC, SHARED_DIR, and ARTIFACT_DIR are required")
    try:
        evaluate(
            args.job_spec,
            shared_dir=Path(args.shared_dir),
            artifact_dir=Path(args.artifact_dir),
        )
    except EvaluationError as exc:
        print(f"gatekeeper evaluation failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":  # pragma: no cover - exercised by the image
    raise SystemExit(main())
