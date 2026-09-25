from __future__ import annotations

import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator

from test_selector.gatekeeper.evaluate import ALLOWLISTED_JOBS


@pytest.fixture(scope="module")
def validator() -> Draft202012Validator:
    path = Path(__file__).parents[1] / "selection.schema.json"
    return Draft202012Validator(json.loads(path.read_text()))


def _selection() -> dict:
    return {
        "schema_version": 1,
        "repository": "opendatahub-io/kserve",
        "pull_number": 123,
        "base_sha": "a" * 40,
        "head_sha": "b" * 40,
        "mode": "selected",
        "jobs": [ALLOWLISTED_JOBS[0]],
        "changed_files": ["pkg/a.go"],
        "reasons": ["pkg/a.go -> graph"],
    }


def test_selection_artifact_schema_accepts_strict_output(validator) -> None:
    assert list(validator.iter_errors(_selection())) == []


def test_fallback_artifact_must_name_every_allowlisted_job(validator) -> None:
    selection = _selection()
    selection["mode"] = "fallback-all"
    selection["jobs"] = list(ALLOWLISTED_JOBS[:-1])

    assert list(validator.iter_errors(selection))


@pytest.mark.parametrize(
    "field,value",
    [
        ("base_sha", "A" * 40),
        ("head_sha", "b" * 39),
        ("mode", "all"),
        ("jobs", ["arbitrary-job"]),
        ("extra", True),
    ],
)
def test_selection_artifact_schema_rejects_untrusted_shape(
    validator, field, value
) -> None:
    selection = _selection()
    selection[field] = value

    assert list(validator.iter_errors(selection))
