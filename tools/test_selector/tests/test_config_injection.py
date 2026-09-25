from __future__ import annotations

import json
from pathlib import Path

import pytest

from test_selector.config_loader import load_config
from test_selector.analyzers.e2e_mapper import analyze_e2e_tests
from test_selector.cli import build_parser
from test_selector.mapping.loader import load_mapping
from test_selector.selector.engine import select_tests


def test_explicit_config_does_not_fall_back_to_repo_override(tmp_path: Path) -> None:
    config_dir = tmp_path / "tools" / "test_selector"
    config_dir.mkdir(parents=True)
    committed = config_dir / "config.json"
    committed.write_text(json.dumps({"keyword_aliases": {"trusted": ["Trusted"]}}))
    (config_dir / "config.override.json").write_text(
        json.dumps({"keyword_aliases": {"untrusted": ["Untrusted"]}})
    )

    config = load_config(committed)

    assert config["keyword_aliases"] == {"trusted": ["Trusted"]}


def test_e2e_mapping_includes_module_and_class_markers() -> None:
    info = analyze_e2e_tests(
        Path(__file__).parents[3],
        known_crd_kinds={"LLMInferenceService", "LLMInferenceServiceConfig"},
    )["test/e2e/llmisvc/test_llm_canary_lifecycle.py"]

    assert {"llminferenceservice", "cluster_cpu"} <= set(info.markers)


def test_cli_accepts_trusted_paths_before_and_after_subcommand() -> None:
    parser = build_parser()

    before = parser.parse_args(
        [
            "--config",
            "/trusted/config.json",
            "learn",
            "--output",
            "/tmp/mapping.json",
        ]
    )
    after = parser.parse_args(
        [
            "query",
            "--mapping",
            "/trusted/mapping.json",
            "--changed-files-file",
            "/tmp/changed.txt",
            "--match-jobs-file",
            "/trusted/jobs.json",
        ]
    )

    assert before.config == "/trusted/config.json"
    assert before.output == "/tmp/mapping.json"
    assert after.mapping == "/trusted/mapping.json"
    assert after.changed_files_file == "/tmp/changed.txt"
    assert after.match_jobs_file == "/trusted/jobs.json"


def test_mapping_loader_rejects_incomplete_mapping(tmp_path: Path) -> None:
    path = tmp_path / "mapping.json"
    path.write_text(json.dumps({"entrypoints": {}}))

    with pytest.raises(SystemExit):
        load_mapping(tmp_path, path)


def test_mapping_loader_rejects_symlink(tmp_path: Path) -> None:
    target = tmp_path / "trusted.json"
    target.write_text("{}")
    link = tmp_path / "mapping.json"
    link.symlink_to(target)

    with pytest.raises(SystemExit):
        load_mapping(tmp_path, link)


def test_unknown_python_path_widens_selection(mapping, repo_root: Path) -> None:
    selection = select_tests(mapping, ["new/source.py"], repo_root)

    assert selection.go_tests.all is True
    assert selection.e2e_tests.run is True


def test_injected_config_controls_selector_rules(mapping, repo_root: Path) -> None:
    selection = select_tests(
        mapping,
        ["generated/source.py"],
        repo_root,
        config={"ignorable_patterns": ["generated/"]},
    )

    assert selection.reasons == []
    assert selection.e2e_tests.run is False
