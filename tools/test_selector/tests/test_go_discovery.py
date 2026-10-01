from __future__ import annotations

from pathlib import Path
from types import SimpleNamespace

import pytest

from test_selector.analyzers import go_deps


def test_go_list_nonzero_is_hard_failure(monkeypatch) -> None:
    monkeypatch.setattr(
        go_deps.subprocess,
        "run",
        lambda *args, **kwargs: SimpleNamespace(
            returncode=1, stdout="{}", stderr="boom"
        ),
    )

    with pytest.raises(go_deps.GoListError):
        go_deps._run_go_list_all(Path("/tmp"))


def test_go_list_timeout_is_hard_failure(monkeypatch) -> None:
    def timeout(*args, **kwargs):
        raise go_deps.subprocess.TimeoutExpired("go list", 120)

    monkeypatch.setattr(go_deps.subprocess, "run", timeout)

    with pytest.raises(go_deps.GoListError, match="timed out"):
        go_deps._run_go_list_all(Path("/tmp"))


def test_go_list_malformed_json_is_hard_failure() -> None:
    with pytest.raises(go_deps.GoListError):
        go_deps._parse_go_list_json('{"ImportPath": "example"} trailing')


def test_empty_internal_package_map_is_hard_failure(monkeypatch, tmp_path) -> None:
    monkeypatch.setattr(go_deps, "_run_go_list_all", lambda repo: [])

    with pytest.raises(go_deps.GoListError):
        go_deps.build_go_dependency_info(tmp_path, "example/", [])


def test_go_list_pins_toolchain_workspace_and_module_mode(monkeypatch) -> None:
    captured = {}

    def fake_run(*args, **kwargs):
        captured.update(kwargs)
        return SimpleNamespace(
            returncode=0,
            stdout='{"ImportPath":"example/pkg","Dir":"/tmp/pkg"}',
            stderr="",
        )

    monkeypatch.setenv("GOTOOLCHAIN", "auto")
    monkeypatch.setenv("GOWORK", "/tmp/untrusted/go.work")
    monkeypatch.setenv("GOFLAGS", "-mod=mod")
    monkeypatch.setattr(go_deps.subprocess, "run", fake_run)

    assert go_deps._run_go_list_all(Path("/tmp"))
    assert captured["env"]["GOTOOLCHAIN"] == "local"
    assert captured["env"]["GOWORK"] == "off"
    assert captured["env"]["GOFLAGS"] == "-buildvcs=false -mod=readonly"
