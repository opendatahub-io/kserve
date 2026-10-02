"""Analyzer results must not depend on the checkout parent directory."""

from __future__ import annotations

from pathlib import Path

import pytest

from test_selector.analyzers.e2e_mapper import analyze_e2e_tests
from test_selector.analyzers.python_imports import discover_python_packages
from test_selector.mapping.schema import Mapping


def test_analyzers_filter_only_paths_inside_repo(tmp_path: Path) -> None:
    repo = tmp_path / "data" / ".venv" / "repo"
    e2e_file = repo / "test" / "e2e" / "graph" / "test_graph.py"
    e2e_file.parent.mkdir(parents=True)
    e2e_file.write_text("import pytest\npytestmark = pytest.mark.graph\n")

    package = repo / "python" / "sklearnserver"
    (package / "sklearnserver").mkdir(parents=True)
    (package / "setup.py").write_text("# test package\n")
    (package / "sklearnserver" / "__init__.py").write_text("")

    e2e = analyze_e2e_tests(repo)
    packages = discover_python_packages(repo)
    assert "graph" in e2e["test/e2e/graph/test_graph.py"].markers
    assert (
        "python/sklearnserver/sklearnserver/__init__.py"
        in packages["sklearnserver"].files
    )


def test_mapping_rejects_empty_e2e_marker_index_with_tests(mapping: Mapping) -> None:
    data = mapping.to_dict()
    data["all_e2e_markers"] = []

    with pytest.raises(ValueError, match="no E2E markers"):
        Mapping.from_dict(data)
