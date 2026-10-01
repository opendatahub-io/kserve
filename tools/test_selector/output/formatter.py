"""Format test selection output as JSON or YAML."""

from __future__ import annotations

import json

import yaml

from ..mapping.schema import TestSelection


def format_selection(selection: TestSelection, fmt: str = "json") -> str:
    """Format a TestSelection as JSON or YAML string."""
    data = selection.to_dict()

    if fmt == "yaml":
        return yaml.safe_dump(data, sort_keys=False).rstrip("\n")

    return json.dumps(data, indent=2)
