import pytest

from execute import DuckDB


@pytest.fixture
def engine():
    database = DuckDB()
    database.load_fixtures()
    try:
        yield database
    finally:
        database.close()


def test_duckdb_does_not_keep_catalog_changes_between_statements(engine):
    engine.rows("CREATE TABLE oracle_scratch (id INT)")
    assert engine.rows(
        "SELECT table_name FROM information_schema.tables "
        "WHERE table_name = 'oracle_scratch'"
    ) == []


def test_duckdb_preserves_fixture_rows_between_statements(engine):
    before = engine.rows("SELECT * FROM t ORDER BY a, b")
    assert before
    engine.rows("DELETE FROM t")
    assert engine.rows("SELECT * FROM t ORDER BY a, b") == before


def test_duckdb_rolls_back_failed_statements(engine):
    with pytest.raises(Exception, match="does not exist"):
        engine.rows("SELECT * FROM missing_oracle_table")
    assert engine.rows("SELECT COUNT(*) FROM t") == [(5,)]
