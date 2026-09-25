"""All configuration tables for the test selector.

Loaded from config.json. When the project evolves (new CRD, test suite,
framework, or server), update config.json instead of this file.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

from ..config_loader import load_config


DEFAULT_CONFIG_PATH = Path(__file__).resolve().parent.parent / "config.json"


def load_selector_config(path: Path | None = None) -> dict[str, Any]:
    """Load an explicitly selected config, defaulting to committed config."""
    return load_config(path or DEFAULT_CONFIG_PATH)


def keyword_aliases(config: dict[str, Any]) -> dict[str, list[str]]:
    return config.get("keyword_aliases", {})


def python_all_e2e_packages(config: dict[str, Any]) -> list[str]:
    return config.get("python", {}).get("all_e2e_packages", [])


def ignorable_patterns(config: dict[str, Any]) -> list[str]:
    return config.get("ignorable_patterns", [])


def __getattr__(name: str) -> object:
    """Keep legacy constants lazy so importing rules reads no repo files."""
    config = load_selector_config()
    if name == "KEYWORD_ALIASES":
        return keyword_aliases(config)
    if name == "ALL_CRD_KINDS":
        return {kind for kinds in keyword_aliases(config).values() for kind in kinds}
    if name == "PYTHON_ALL_E2E_PACKAGES":
        return python_all_e2e_packages(config)
    if name == "IGNORABLE_PATTERNS":
        return ignorable_patterns(config)
    raise AttributeError(name)
