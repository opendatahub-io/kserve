"""CLI interface for the test-selector tool."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .config_loader import load_config


_STRUCTURED_JOB_FIELDS = frozenset({"command", "context", "target", "expression"})
_MAX_MATCH_JOB_REASONS = 200
_MAX_MATCH_JOB_REASON_LENGTH = 500


def find_repo_root(start: Path | None = None) -> Path:
    """Walk up from start to find the repo root (contains go.mod)."""
    current = (start or Path.cwd()).resolve()
    while current != current.parent:
        if (current / "go.mod").exists() and (current / "pkg").is_dir():
            return current
        current = current.parent
    raise SystemExit("Could not find repo root (no go.mod found)")


def _match_job_expressions(raw: object, *, allow_legacy: bool) -> dict[str, str]:
    """Normalize a trusted job map to the selector expression for each job.

    Gatekeeper's packaged map carries Prow command metadata alongside the
    expression.  Only the expression crosses into the matcher.  The inline
    ``--match-jobs`` option retains its historical string shorthand; files
    used by Gatekeeper must use the complete structured records.
    """
    if not isinstance(raw, dict):
        raise SystemExit("match jobs must be a JSON object")
    if not raw:
        raise SystemExit("match jobs must not be empty")

    expressions: dict[str, str] = {}
    for job_name, record in raw.items():
        if not isinstance(job_name, str) or not job_name:
            raise SystemExit("match jobs must use non-empty string names")
        if isinstance(record, dict):
            if set(record) != _STRUCTURED_JOB_FIELDS:
                raise SystemExit(
                    f"match jobs entry {job_name!r} must contain exactly "
                    "command, context, target, and expression"
                )
            expression = record["expression"]
        elif allow_legacy and isinstance(record, str):
            expression = record
        else:
            raise SystemExit(
                f"match jobs entry {job_name!r} must contain a string expression"
            )
        if not isinstance(expression, str) or not expression.strip():
            raise SystemExit(f"match jobs entry {job_name!r} has an invalid expression")
        expressions[job_name] = expression
    return expressions


def _read_match_jobs(path: str | Path, *, allow_legacy: bool) -> dict[str, str]:
    """Read and validate a JSON job map before evaluating its expressions."""
    try:
        raw = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SystemExit(f"could not read match jobs: {path}") from exc
    return _match_job_expressions(raw, allow_legacy=allow_legacy)


def _bounded_selection_reasons(selection: object) -> list[str]:
    """Keep selector diagnostics bounded for the evaluator line protocol."""
    reasons = getattr(selection, "reasons", [])
    if not isinstance(reasons, list):
        return []
    return [
        str(reason)[:_MAX_MATCH_JOB_REASON_LENGTH]
        for reason in reasons[:_MAX_MATCH_JOB_REASONS]
    ]


def cmd_learn(args: argparse.Namespace) -> None:
    from .mapping.builder import build_mapping

    repo_root = find_repo_root(Path(args.repo) if args.repo else None)
    config_arg = getattr(args, "config", None)
    config_path = Path(config_arg) if config_arg else None
    mapping = build_mapping(repo_root, config_path=config_path)

    output_path = (
        Path(args.output).resolve()
        if getattr(args, "output", None)
        else repo_root / "tools" / "test_selector" / "mapping.json"
    )
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(json.dumps(mapping.to_dict(), indent=2) + "\n")
    print(f"Mapping written to {output_path}", file=sys.stderr)

    ep_count = len(mapping.entrypoints)
    test_count = len(mapping.test_files)
    pkg_count = len(mapping.go_file_to_package)
    print(
        f"Discovered: {ep_count} entrypoints, {pkg_count} Go file mappings, "
        f"{test_count} test files",
        file=sys.stderr,
    )


def cmd_query(args: argparse.Namespace) -> None:
    from .mapping.loader import load_mapping
    from .output.formatter import format_selection
    from .selector.engine import select_tests
    from .selector.matcher import expression_matches, extract_positive_markers

    repo_root = find_repo_root(Path(args.repo) if args.repo else None)
    config_arg = getattr(args, "config", None)
    config_path = Path(config_arg) if config_arg else None
    config = load_config(config_path) if config_path else None

    changed_files_file = getattr(args, "changed_files_file", None)
    if changed_files_file:
        changed_files = [
            line.strip()
            for line in Path(changed_files_file).read_text().splitlines()
            if line.strip()
        ]
    elif args.changed_files:
        changed_files = args.changed_files
    else:
        changed_files = [line.strip() for line in sys.stdin if line.strip()]

    match_jobs = getattr(args, "match_jobs", None)
    match_jobs_file = getattr(args, "match_jobs_file", None)
    if not changed_files and args.match is None and not (match_jobs or match_jobs_file):
        print("{}", file=sys.stdout)
        return

    mapping_arg = getattr(args, "mapping", None)
    mapping_path = Path(mapping_arg) if mapping_arg else None
    mapping = load_mapping(repo_root, mapping_path)
    selection = select_tests(
        mapping,
        changed_files,
        repo_root,
        config=config,
        config_path=config_path,
    )

    if match_jobs is not None or match_jobs_file is not None:
        selected = set(selection.e2e_tests.markers)
        if match_jobs_file is not None:
            jobs = _read_match_jobs(match_jobs_file, allow_legacy=False)
        else:
            try:
                raw_jobs = json.loads(match_jobs)
            except json.JSONDecodeError as exc:
                raise SystemExit("match jobs is not valid JSON") from exc
            jobs = _match_job_expressions(raw_jobs, allow_legacy=True)
        for job_name, expr in jobs.items():
            matched = expression_matches(expr, selected)
            print(f"{job_name}={str(matched).lower()}")
        print(
            json.dumps(
                {"reasons": _bounded_selection_reasons(selection)},
                separators=(",", ":"),
            )
        )
        return

    if args.match is not None:
        selected = set(selection.e2e_tests.markers)
        expr_markers = extract_positive_markers(args.match)
        matched = expression_matches(args.match, selected)
        result = {
            **selection.to_dict(),
            "match": matched,
            "match_details": {
                "expression": args.match,
                "expression_markers": sorted(expr_markers),
                "matched_markers": sorted(expr_markers & selected),
            },
        }
        print(json.dumps(result, indent=2), file=sys.stdout)
        if matched:
            print(
                f"MATCH: expression has markers in selected set "
                f"({', '.join(sorted(expr_markers & selected))})",
                file=sys.stderr,
            )
        else:
            print(
                f"NO MATCH: none of {sorted(expr_markers)} in selected markers",
                file=sys.stderr,
            )
        sys.exit(0 if matched else 1)

    print(format_selection(selection, args.format), file=sys.stdout)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="test-selector",
        description="CI-agnostic test selector using AST and dependency analysis",
    )
    parser.add_argument(
        "--repo",
        default=None,
        help="Path to repo root (auto-detected if omitted)",
    )
    parser.add_argument(
        "--config",
        default=None,
        help="Trusted selector config path (defaults to committed config.json)",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    learn_parser = sub.add_parser("learn", help="Scan repo and build mapping.json")
    learn_parser.add_argument(
        "--repo",
        default=argparse.SUPPRESS,
        help="Path to repo root (may also be supplied globally)",
    )
    learn_parser.add_argument(
        "--output",
        default=None,
        help="Mapping output path (defaults to tools/test_selector/mapping.json)",
    )

    query_parser = sub.add_parser(
        "query",
        help="Select tests for changed files (reads from stdin or --changed-files)",
    )
    query_parser.add_argument(
        "--repo",
        default=argparse.SUPPRESS,
        help="Path to repo root (may also be supplied globally)",
    )
    query_parser.add_argument(
        "--changed-files",
        nargs="*",
        default=None,
        help="Changed file paths (reads stdin if omitted)",
    )
    query_parser.add_argument(
        "--mapping",
        default=None,
        help="Trusted mapping path (defaults to repo mapping.json)",
    )
    query_parser.add_argument(
        "--changed-files-file",
        default=None,
        help="Read changed paths, one per line, from this file",
    )
    query_parser.add_argument(
        "--format",
        choices=["json", "yaml"],
        default="json",
        help="Output format (default: json)",
    )
    query_parser.add_argument(
        "--match-jobs-file",
        default=None,
        help=(
            "Trusted JSON job map with command, context, target, and "
            "expression records (string shorthand is rejected)"
        ),
    )
    query_parser.add_argument(
        "--match",
        default=None,
        help="Marker expression to evaluate against selected markers "
        "(exit 0 if match, 1 if no match)",
    )
    query_parser.add_argument(
        "--match-jobs",
        default=None,
        help="JSON object mapping job names to marker expressions. "
        "String values are retained for inline compatibility; outputs "
        "job=true/false lines followed by bounded JSON reasons.",
    )

    return parser


def main() -> None:
    parser = build_parser()
    args = parser.parse_args()

    if args.command == "learn":
        cmd_learn(args)
    elif args.command == "query":
        cmd_query(args)
