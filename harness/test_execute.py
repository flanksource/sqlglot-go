from itertools import cycle

import pytest

from execute import Engine, run


class ScriptedEngine(Engine):
    name = "scripted"
    dialect = "duckdb"

    def __init__(self, source, rewritten):
        self.results = {"source": cycle(source), "rewritten": cycle(rewritten)}

    def rows(self, sql):
        return next(self.results[sql])


@pytest.mark.parametrize(
    "ordered,source,rewritten,verdict",
    [
        (False, [[(1,), (2,)], [(2,), (1,)]], [[(2,), (1,)]], "reference: agree"),
        (False, [[(1,), (1,), (2,)]], [[(1,), (2,), (2,)]], "DIVERGED"),
        (True, [[(1,), (2,)]], [[(2,), (1,)]], "DIVERGED"),
        (True, [[(1,), (2,)], [(2,), (1,)]], [[(1,), (2,)]], "nondeterministic"),
        (False, [[(1,)], [(2,)]], [[(1,)]], "nondeterministic"),
        (False, [[(1,)]], [[(1,)], [(2,)]], "reference: nondeterministic"),
        (False, [[([1, 2],)], [([2, 1],)]], [[([1, 2],)]], "nondeterministic"),
    ],
)
def test_execution_respects_ordering_and_preserves_value_differences(
    ordered, source, rewritten, verdict
):
    pairs = [{"dialect": "duckdb", "sql": "source", "reference": "rewritten", "ordered": ordered}]
    tally, divergences = run(ScriptedEngine(source, rewritten), pairs, set())
    expected = {verdict: 1}
    if verdict != "nondeterministic":
        expected["port refused it"] = 1
    assert tally == expected
    assert len(divergences) == (1 if verdict == "DIVERGED" else 0)
