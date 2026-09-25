from __future__ import annotations

import json
import importlib
import subprocess
from pathlib import Path

import pytest

from test_selector.gatekeeper.evaluate import (
    CommandFailure,
    CommandResult,
    InvalidJobSpec,
    MergeConflict,
    StaleJobIdentity,
    _parse_selector_output,
    evaluate,
    parse_job_spec,
)

evaluator = importlib.import_module("test_selector.gatekeeper.evaluate")


class FakeRunner:
    def __init__(
        self,
        *,
        head: str,
        merge_status: int = 0,
        selector_status: int = 0,
        diff_output: str = "M\tpkg/a.py\nM\tpkg/b.py\n",
    ):
        self.head = head
        self.merge_status = merge_status
        self.selector_status = selector_status
        self.diff_output = diff_output
        self.commands: list[tuple[list[str], Path | None]] = []
        self.environments: list[dict[str, str] | None] = []

    def run(self, command, *, cwd=None, env=None):
        command = list(command)
        self.commands.append((command, cwd))
        self.environments.append(env)
        if command[:2] == ["git", "rev-parse"]:
            return CommandResult(0, self.head + "\n")
        if command[:2] == ["git", "merge"]:
            return CommandResult(self.merge_status, "", "conflict")
        if command[:2] == ["git", "diff"]:
            return CommandResult(0, self.diff_output)
        if "query" in command:
            return CommandResult(
                self.selector_status,
                "e2e-graph=true\ne2e-raw=false\ne2e-predictor=true\ne2e-llm-inference-service=false\n",
                "selector failed",
            )
        return CommandResult(0)


def _job_spec(*, base_sha: str, head_sha: str, pulls: list[dict] | None = None) -> dict:
    return {
        "type": "presubmit",
        "refs": {
            "org": "opendatahub-io",
            "repo": "kserve",
            "base_ref": "master",
            "base_sha": base_sha,
            "pulls": pulls if pulls is not None else [{"number": 123, "sha": head_sha}],
        },
    }


@pytest.mark.parametrize("job_type", [None, "periodic", "postsubmit", ""])
def test_evaluate_rejects_non_presubmit_job_spec_before_output(
    tmp_path: Path, job_type: str | None
) -> None:
    spec = _job_spec(base_sha="a" * 40, head_sha="b" * 40)
    if job_type is None:
        del spec["type"]
    else:
        spec["type"] = job_type
    runner = FakeRunner(head="b" * 40)

    with pytest.raises(InvalidJobSpec, match="presubmit"):
        evaluate(
            spec,
            shared_dir=tmp_path / "shared",
            artifact_dir=tmp_path / "artifacts",
            runner=runner,
        )

    assert runner.commands == []
    assert not (tmp_path / "shared" / "selection.json").exists()


def _structured_job_map() -> dict[str, dict[str, str]]:
    targets = {
        "e2e-graph": "e2e-graph-ocp",
        "e2e-raw": "e2e-raw-ocp",
        "e2e-predictor": "e2e-predictor-ocp",
        "e2e-llm-inference-service": "e2e-llmisvc-ocp",
    }
    expressions = {
        "e2e-graph": "graph",
        "e2e-raw": "raw or rawcipn",
        "e2e-predictor": "predictor or kserve_on_openshift",
        "e2e-llm-inference-service": "llmisvc_core and cluster_cpu and not pvc_storage",
    }
    return {
        name: {
            "command": f"/test {name}",
            "context": f"ci/prow/{name}",
            "target": targets[name],
            "expression": expressions[name],
        }
        for name in evaluator.ALLOWLISTED_JOBS
    }


def test_parse_job_spec_accepts_one_lowercase_full_sha_pull() -> None:
    base = "a" * 40
    head = "b" * 40

    identity = parse_job_spec(_job_spec(base_sha=base, head_sha=head))

    assert identity.repository == "opendatahub-io/kserve"
    assert identity.pull_number == 123
    assert identity.base_sha == base
    assert identity.head_sha == head


@pytest.mark.parametrize(
    "mutate",
    [
        lambda spec: spec["refs"].update(org="someone-else"),
        lambda spec: spec["refs"].update(base_ref="main"),
        lambda spec: spec["refs"].update(base_sha="A" * 40),
        lambda spec: spec["refs"].update(base_sha="a" * 39),
        lambda spec: spec["refs"]["pulls"].append({"number": 124, "sha": "c" * 40}),
        lambda spec: spec["refs"]["pulls"][0].update(sha="not-a-sha"),
    ],
)
def test_parse_job_spec_rejects_ambiguous_or_untrusted_identity(mutate) -> None:
    spec = _job_spec(base_sha="a" * 40, head_sha="b" * 40)
    mutate(spec)

    with pytest.raises(InvalidJobSpec):
        parse_job_spec(spec)


def test_evaluate_verifies_fetched_head_and_writes_atomic_selection(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    base = "a" * 40
    head = "b" * 40
    trusted_map = tmp_path / "ocp_jobs.json"
    trusted_map.write_text(json.dumps(_structured_job_map()))
    monkeypatch.setattr(evaluator, "DEFAULT_JOB_MAP", trusted_map)
    runner = FakeRunner(head=head)

    result = evaluate(
        _job_spec(base_sha=base, head_sha=head),
        shared_dir=tmp_path / "shared",
        artifact_dir=tmp_path / "artifacts",
        runner=runner,
        work_dir=tmp_path / "work",
    )

    assert result.mode == "selected", result.reasons
    assert result.jobs == ("e2e-graph", "e2e-predictor")
    output = json.loads((tmp_path / "shared" / "selection.json").read_text())
    assert output["head_sha"] == head
    assert output == json.loads((tmp_path / "artifacts" / "selection.json").read_text())
    clone = runner.commands[0][0]
    assert clone[:4] == ["git", "clone", "--filter=blob:none", "--no-checkout"]
    assert clone[4] == "https://github.com/opendatahub-io/kserve.git"
    fetch_commands = [
        command for command, _ in runner.commands if command[:2] == ["git", "fetch"]
    ]
    assert fetch_commands[1][-1].startswith(
        "+refs/pull/123/head:refs/remotes/origin/pull/123/head"
    )
    assert all(
        "token" not in part.lower()
        for command, _ in runner.commands
        for part in command
    )


def test_git_and_selector_commands_receive_no_credential_environment(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("GITHUB_TOKEN", "must-not-cross-the-boundary")
    runner = FakeRunner(head="b" * 40)

    evaluate(
        _job_spec(base_sha="a" * 40, head_sha="b" * 40),
        shared_dir=tmp_path / "shared",
        artifact_dir=tmp_path / "artifacts",
        runner=runner,
        work_dir=tmp_path / "work",
    )

    assert all(
        env is not None and "GITHUB_TOKEN" not in env for env in runner.environments
    )
    selector_envs = [
        env
        for (command, _), env in zip(runner.commands, runner.environments, strict=True)
        if command and command[0] == evaluator.sys.executable
    ]
    assert selector_envs
    assert all(env["GOFLAGS"] == "-buildvcs=false" for env in selector_envs)
    assert all(env["GOTOOLCHAIN"] == "local" for env in selector_envs)
    assert all(env["GOCACHE"].startswith("/tmp/") for env in selector_envs)


def test_fetched_head_mismatch_is_stale_identity_without_selection_artifact(
    tmp_path: Path,
) -> None:
    runner = FakeRunner(head="c" * 40)
    with pytest.raises(StaleJobIdentity, match="fetched pull ref"):
        evaluate(
            _job_spec(base_sha="a" * 40, head_sha="b" * 40),
            shared_dir=tmp_path / "shared",
            artifact_dir=tmp_path / "artifacts",
            runner=runner,
            work_dir=tmp_path / "work",
        )
    assert not (tmp_path / "shared" / "selection.json").exists()
    assert not (tmp_path / "artifacts" / "selection.json").exists()


def test_changed_file_diagnostics_cover_add_modify_rename_and_delete(
    tmp_path: Path,
) -> None:
    runner = FakeRunner(
        head="b" * 40,
        diff_output="A\tnew.py\nM\tmodified.py\nR100\told.py\tnew-name.py\nD\tdeleted.py\n",
    )

    result = evaluate(
        _job_spec(base_sha="a" * 40, head_sha="b" * 40),
        shared_dir=tmp_path / "shared",
        artifact_dir=tmp_path / "artifacts",
        runner=runner,
        work_dir=tmp_path / "work",
    )

    assert result.changed_files == (
        "new.py",
        "modified.py",
        "old.py",
        "new-name.py",
        "deleted.py",
    )
    diff_command = next(
        command for command, _ in runner.commands if command[:2] == ["git", "diff"]
    )
    assert diff_command[:4] == ["git", "diff", "--name-status", "--find-renames"]


@pytest.mark.parametrize(
    "diff_output",
    [
        "R100\told.py\n",
        "C100\told.py\n",
        "R100\told.py\t\n",
        "M\tfile.py\textra\n",
    ],
)
def test_malformed_rename_copy_or_status_record_widens_conservatively(
    tmp_path: Path, diff_output: str
) -> None:
    result = evaluate(
        _job_spec(base_sha="a" * 40, head_sha="b" * 40),
        shared_dir=tmp_path / "shared",
        artifact_dir=tmp_path / "artifacts",
        runner=FakeRunner(head="b" * 40, diff_output=diff_output),
        work_dir=tmp_path / "work",
    )
    assert result.mode == "fallback-all"
    assert "incomplete" in result.reasons[0]


def test_selector_output_requires_one_result_for_every_allowlisted_job() -> None:
    output = "e2e-graph=true\ne2e-raw=false\ne2e-predictor=true\n"

    with pytest.raises(CommandFailure, match="incomplete job results"):
        _parse_selector_output(output)


@pytest.mark.parametrize(
    "duplicate",
    [
        "e2e-graph=true\ne2e-graph=true\n",
        "e2e-graph=true\ne2e-graph=false\n",
    ],
)
def test_selector_output_rejects_duplicate_job_results(duplicate: str) -> None:
    output = duplicate + (
        "e2e-raw=false\ne2e-predictor=false\ne2e-llm-inference-service=false\n"
    )

    with pytest.raises(CommandFailure, match="duplicate job result"):
        _parse_selector_output(output)


def test_packaged_job_map_is_read_only_and_missing_map_falls_back(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    packaged_map = tmp_path / "ocp_jobs.json"
    packaged_map.write_text(json.dumps(_structured_job_map()))
    before = packaged_map.read_bytes()
    monkeypatch.setattr(evaluator, "DEFAULT_JOB_MAP", packaged_map)
    runner = FakeRunner(head="b" * 40)

    result = evaluate(
        _job_spec(base_sha="a" * 40, head_sha="b" * 40),
        shared_dir=tmp_path / "shared",
        artifact_dir=tmp_path / "artifacts",
        runner=runner,
        work_dir=tmp_path / "work",
    )

    assert result.mode == "selected"
    assert packaged_map.read_bytes() == before

    packaged_map.unlink()
    fallback = evaluate(
        _job_spec(base_sha="a" * 40, head_sha="b" * 40),
        shared_dir=tmp_path / "shared-missing",
        artifact_dir=tmp_path / "artifacts-missing",
        runner=FakeRunner(head="b" * 40),
        work_dir=tmp_path / "work-missing",
    )
    assert fallback.mode == "fallback-all"

    packaged_map.write_text("{}")
    invalid = evaluate(
        _job_spec(base_sha="a" * 40, head_sha="b" * 40),
        shared_dir=tmp_path / "shared-invalid",
        artifact_dir=tmp_path / "artifacts-invalid",
        runner=FakeRunner(head="b" * 40),
        work_dir=tmp_path / "work-invalid",
    )
    assert invalid.mode == "fallback-all"


def _git(repo: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", *args], cwd=repo, check=True, capture_output=True, text=True
    )
    return result.stdout.strip()


def test_evaluate_runs_real_selector_on_local_merged_worktree(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = tmp_path / "source"
    source.mkdir()
    _git(source, "init", "--initial-branch=master")
    _git(source, "config", "user.email", "gatekeeper@example.invalid")
    _git(source, "config", "user.name", "Gatekeeper Test")
    (source / "go.mod").write_text("module example.com/kserve\n\ngo 1.20\n")
    (source / "pkg" / "example").mkdir(parents=True)
    (source / "pkg" / "example" / "example.go").write_text(
        'package example\n\nconst Name = "base"\n'
    )
    graph_test = source / "test" / "e2e" / "graph" / "test_graph.py"
    graph_test.parent.mkdir(parents=True)
    graph_test.write_text("import pytest\n\npytestmark = pytest.mark.graph\n")
    _git(source, "add", ".")
    _git(
        source,
        "-c",
        "core.hooksPath=/dev/null",
        "-c",
        "commit.gpgSign=false",
        "commit",
        "-m",
        "base",
    )
    base = _git(source, "rev-parse", "HEAD")
    graph_test.write_text(
        "import pytest\n\npytestmark = pytest.mark.graph\n# changed\n"
    )
    _git(source, "add", ".")
    _git(
        source,
        "-c",
        "core.hooksPath=/dev/null",
        "-c",
        "commit.gpgSign=false",
        "commit",
        "-m",
        "pull",
    )
    head = _git(source, "rev-parse", "HEAD")
    marker = tmp_path / "shadowed-selector-executed"
    malicious = source / "test_selector" / "__main__.py"
    malicious.parent.mkdir()
    malicious.write_text(
        "from pathlib import Path\n"
        f"Path({str(marker)!r}).write_text('executed')\n"
        "raise SystemExit('PR-controlled selector shadowed trusted package')\n"
    )
    graph_test.write_text(
        "import pytest\n\npytestmark = pytest.mark.graph\n# changed\n"
    )
    _git(source, "add", ".")
    _git(
        source,
        "-c",
        "core.hooksPath=/dev/null",
        "-c",
        "commit.gpgSign=false",
        "commit",
        "--amend",
        "--no-edit",
    )
    head = _git(source, "rev-parse", "HEAD")
    _git(source, "update-ref", "refs/pull/42/head", head)

    trusted_map = tmp_path / "ocp_jobs.json"
    trusted_map.write_text(json.dumps(_structured_job_map()))
    trusted_config = tmp_path / "config.json"
    config = json.loads(evaluator.DEFAULT_CONFIG.read_text())
    config["ignorable_patterns"].append("test_selector/")
    trusted_config.write_text(json.dumps(config))
    monkeypatch.setattr(evaluator, "CLONE_URL", str(source))
    # Even if the ambient import path points into the PR checkout, the
    # evaluator must retain the image-owned selector package.
    monkeypatch.setenv("PYTHONPATH", str(source))

    result = evaluate(
        _job_spec(base_sha=base, head_sha=head, pulls=[{"number": 42, "sha": head}]),
        shared_dir=tmp_path / "shared",
        artifact_dir=tmp_path / "artifacts",
        config_path=trusted_config,
        job_map_path=trusted_map,
        work_dir=tmp_path / "work",
    )

    assert result.mode == "selected", result.reasons
    assert result.jobs == ("e2e-graph",)
    assert result.changed_files == (
        "test/e2e/graph/test_graph.py",
        "test_selector/__main__.py",
    )
    assert not marker.exists()
    artifact = json.loads((tmp_path / "shared" / "selection.json").read_text())
    assert artifact["jobs"] == ["e2e-graph"]
    assert artifact["head_sha"] == head


def test_merge_conflict_fails_without_dispatchable_output(tmp_path: Path) -> None:
    runner = FakeRunner(head="b" * 40, merge_status=1)

    with pytest.raises(MergeConflict):
        evaluate(
            _job_spec(base_sha="a" * 40, head_sha="b" * 40),
            shared_dir=tmp_path / "shared",
            artifact_dir=tmp_path / "artifacts",
            runner=runner,
            work_dir=tmp_path / "work",
        )

    assert not (tmp_path / "shared" / "selection.json").exists()


def test_invalid_identity_fails_before_running_or_writing(tmp_path: Path) -> None:
    runner = FakeRunner(head="b" * 40)
    invalid = _job_spec(base_sha="A" * 40, head_sha="b" * 40)

    with pytest.raises(InvalidJobSpec):
        evaluate(
            invalid,
            shared_dir=tmp_path / "shared",
            artifact_dir=tmp_path / "artifacts",
            runner=runner,
        )

    assert runner.commands == []
    assert not (tmp_path / "shared").exists()
