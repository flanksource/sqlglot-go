import itertools
import json

import pytest

import execute
from execute import Engine, run


class RepeatingEngine(Engine):
    name = "test"
    dialect = "test"

    def __init__(self, results):
        self.results = {sql: itertools.cycle(rows) for sql, rows in results.items()}

    def rows(self, sql):
        return next(self.results[sql])


@pytest.mark.parametrize(
    "source, rewritten, expected",
    [
        (
            [[(1,), (2,)], [(2,), (1,)]],
            [[(1,), (2,)]],
            {"reference: agree": 1, "port: agree": 1},
        ),
        (
            [[(1,), (2,)]],
            [[(1,), (2,)], [(2,), (1,)], [(2,), (1,)], [(1,), (2,)]],
            {"reference: agree": 1, "port: same rows, different order": 1},
        ),
        (
            [[(1,)], [(2,)]],
            [[(1,)]],
            {"nondeterministic": 1},
        ),
        (
            [[(1,)]],
            [[(1,)], [(2,)]],
            {"reference: nondeterministic": 1, "port: nondeterministic": 1},
        ),
        (
            [[(1,), (1,)], [(1,)]],
            [[(1,)]],
            {"nondeterministic": 1},
        ),
        (
            [[([1, 2],)], [([2, 1],)]],
            [[([1, 2],)]],
            {"nondeterministic": 1},
        ),
    ],
    ids=["source-row-order", "rewritten-row-order", "source-values", "rewritten-values",
         "duplicate-count", "array-value-order"],
)
def test_repeat_checks_distinguish_row_order_from_changing_values(source, rewritten, expected):
    engine = RepeatingEngine({"source": source, "rewritten": rewritten})
    pair = {"dialect": engine.dialect, "sql": "source", "reference": "rewritten", "port": "rewritten"}

    assert run(engine, [pair], set()) == (expected, [])


def test_reordered_repeats_still_report_different_rows():
    engine = RepeatingEngine({
        "source": [[(1,), (2,)], [(2,), (1,)]],
        "rewritten": [[(1,), (3,)], [(3,), (1,)]],
    })
    pair = {"dialect": engine.dialect, "sql": "source", "reference": "rewritten"}

    assert run(engine, [pair], set()) == (
        {"DIVERGED": 1, "port refused it": 1},
        [{"engine": "test", "sql": "source", "who": "reference", "written": "rewritten",
          "why": "[(1,), (2,)] != [(1,), (3,)]"}],
    )


@pytest.mark.parametrize("floor, verdict", [(2, 0), (3, 1)], ids=["floor-met", "below-floor"])
def test_reordered_rows_count_toward_execution_coverage(tmp_path, monkeypatch, capsys, floor, verdict):
    engine = RepeatingEngine({
        "source": [[(1,), (2,)]],
        "rewritten": [[(2,), (1,)]],
    })
    pairs = tmp_path / "pairs.jsonl"
    pairs.write_text(json.dumps({
        "dialect": engine.dialect, "sql": "source", "reference": "rewritten", "port": "rewritten",
    }) + "\n")
    gate = tmp_path / "gate.json"
    gate.write_text(json.dumps({"comparable_floor": {engine.name: floor}}))
    monkeypatch.setattr(execute, "open_engines", lambda dsn: ([engine], []))
    monkeypatch.setattr(execute.sys, "argv", ["execute.py", "--pairs", str(pairs), "--gate", str(gate)])

    assert execute.main() == verdict
    output = capsys.readouterr().out
    if verdict == 0:
        assert "2 statements executed and compared across 1 engine(s)." in output
    else:
        assert "EXECUTION COVERAGE REGRESSED for test: 2 compared, floor 3" in output
