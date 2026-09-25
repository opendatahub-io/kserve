"""Load mapping.json and merge with overrides at query time."""

from __future__ import annotations

import json
import sys
from pathlib import Path

from ..mapping.schema import Mapping


def load_mapping(repo_root: Path, mapping_path: Path | None = None) -> Mapping:
    """Load and validate a pre-computed mapping from an explicit path."""
    mapping_path = (
        mapping_path or repo_root / "tools" / "test_selector" / "mapping.json"
    )

    if mapping_path.is_symlink():
        print(f"Error: refusing symlink mapping path: {mapping_path}", file=sys.stderr)
        raise SystemExit(1)

    if not mapping_path.exists():
        print(
            "Error: mapping.json not found. Run 'learn' first.",
            file=sys.stderr,
        )
        raise SystemExit(1)

    try:
        data = json.loads(mapping_path.read_text())
        return Mapping.from_dict(data)
    except (
        OSError,
        json.JSONDecodeError,
        ValueError,
        TypeError,
        KeyError,
        AttributeError,
    ) as exc:
        print(f"Error: invalid mapping {mapping_path}: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
