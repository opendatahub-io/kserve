"""Unit tests for upgrade helper functions."""

import json

import pytest

from upgrade.utils import (
    _parse_probe_records,
    _probe_failures_after_baseline,
    assert_operand_pods_not_recreated,
    assert_restart_counts_not_increased,
)


class TestParseProbeRecords:
    def test_parses_valid_records(self):
        logs = "\n".join(
            [
                '{"ts":"t1","target":"isvc","status":0,"ok":false}',
                '{"ts":"t2","target":"isvc","status":200,"ok":true}',
            ]
        )
        records, malformed = _parse_probe_records(logs)
        assert len(records) == 2
        assert malformed == []

    def test_malformed_records_are_reported(self):
        logs = '{"ts":"t1","target":"isvc","status":0000,"ok":false}'
        records, malformed = _parse_probe_records(logs)
        assert records == []
        assert malformed == [logs]

    def test_ignores_blank_lines(self):
        records, malformed = _parse_probe_records("\n\n")
        assert records == []
        assert malformed == []


class TestAssertRestartCountsNotIncreased:
    def test_missing_pod_fails_when_required(self):
        with pytest.raises(AssertionError, match="no longer present"):
            assert_restart_counts_not_increased(
                {"pod-a": 0},
                {},
                require_baseline_pods=True,
            )

    def test_missing_pod_allowed_for_module_controller(self):
        assert_restart_counts_not_increased(
            {"pod-a": 0},
            {},
            require_baseline_pods=False,
        )

    def test_restart_increase_fails(self):
        with pytest.raises(AssertionError, match="restart count increased"):
            assert_restart_counts_not_increased({"pod-a": 0}, {"pod-a": 1})


class TestProbeFailuresAfterBaseline:
    def test_ignores_failures_before_first_success(self):
        records = [
            {"ok": False, "status": 0},
            {"ok": True, "status": 200},
            {"ok": False, "status": 503},
        ]
        baseline_idx, failures = _probe_failures_after_baseline(records)
        assert baseline_idx == 1
        assert len(failures) == 1
        assert failures[0]["status"] == 503

    def test_no_baseline_returns_none(self):
        records = [{"ok": False, "status": 0}]
        baseline_idx, failures = _probe_failures_after_baseline(records)
        assert baseline_idx is None
        assert failures == []


class TestAssertOperandPodsNotRecreated:
    def test_missing_baseline_pod_fails(self):
        with pytest.raises(AssertionError, match="Operand pod UIDs changed"):
            assert_operand_pods_not_recreated(["uid-a", "uid-b"], ["uid-a"])

    def test_new_pod_fails(self):
        with pytest.raises(AssertionError):
            assert_operand_pods_not_recreated(["uid-a"], ["uid-b"])

    def test_matching_sets_pass(self):
        assert_operand_pods_not_recreated(["uid-a"], ["uid-a"])
