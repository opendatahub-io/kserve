"""Map config/ and chart/ changes to test suites using pattern matching."""

from __future__ import annotations

from fnmatch import fnmatch
from pathlib import Path

from ..selector.rules import load_selector_config


def load_overrides(repo_root: Path, config: dict | None = None) -> list[dict]:
    """Return overrides from the caller-selected config.

    ``repo_root`` remains part of the API for compatibility, but no config
    override is discovered from the repository implicitly.
    """
    del repo_root
    data = config if config is not None else load_selector_config()
    overrides = data.get("overrides", [])
    return overrides if isinstance(overrides, list) else []


def match_override(file_path: str, overrides: list[dict]) -> list[dict]:
    """Find all overrides matching a file path."""
    matches = []
    for override in overrides:
        pattern = override.get("pattern", "")
        if _matches_pattern(file_path, pattern):
            matches.append(override)
    return matches


def _matches_pattern(file_path: str, pattern: str) -> bool:
    """Check if file_path matches a glob-like pattern."""
    if pattern.endswith("/**"):
        prefix = pattern[:-3]
        return file_path.startswith(prefix + "/") or file_path == prefix
    if pattern.endswith("/*"):
        prefix = pattern[:-2]
        return (
            file_path.startswith(prefix + "/")
            and "/" not in file_path[len(prefix) + 1 :]
        )
    if "*" in pattern:
        return fnmatch(file_path, pattern)
    return file_path == pattern
