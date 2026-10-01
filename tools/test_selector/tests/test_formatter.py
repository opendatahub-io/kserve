"""Selection output preserves changed file paths in YAML."""

from __future__ import annotations

import yaml

from test_selector.mapping.schema import TestSelection as Selection
from test_selector.output.formatter import format_selection


def test_yaml_output_round_trips_adversarial_path() -> None:
    selection = Selection()
    selection.reasons.append('test/e2e/weird"\\name\n #suffix.py -> all')

    output = format_selection(selection, "yaml")

    assert yaml.safe_load(output) == selection.to_dict()
    assert not output.endswith("\n")
