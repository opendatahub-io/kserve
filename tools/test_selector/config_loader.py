"""Load selector configuration selected explicitly by the caller."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any


def load_config(path: Path) -> dict[str, Any]:
    """Load one explicit selector configuration file.

    Configuration is injected by the caller so trusted CI never discovers a
    PR-controlled override as a side effect of importing a module.
    """
    if path.is_symlink():
        raise ValueError(f"refusing symlink selector config {path}")
    try:
        data = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"could not load selector config {path}: {exc}") from exc
    if not isinstance(data, dict):
        raise ValueError(f"selector config {path} must contain a JSON object")
    return data
