package sqlserver

import "ariga.io/atlas/sql/schema"

type (
	// Identity is an IDENTITY(seed, increment) column.
	Identity struct {
		schema.Attr
		Seed, Increment int64
	}

	// DefaultConstraint names the constraint holding a column default. The
	// inspector sets it from the catalog; the planner names new defaults
	// DF_<table>_<column>.
	DefaultConstraint struct {
		schema.Attr
		Name string
		// SystemNamed marks a name SQL Server generated; it is not compared,
		// and a new default for it is created unnamed.
		SystemNamed bool
	}

	// Clustered marks a clustered index or primary key. Its absence means
	// nonclustered.
	Clustered struct {
		schema.Attr
	}

	// IndexInclude lists the non-key columns an index INCLUDEs.
	IndexInclude struct {
		schema.Attr
		Columns []string
	}

	// IndexPredicate is a filtered index's WHERE clause.
	IndexPredicate struct {
		schema.Attr
		P string
		// normalized is P's canonical form, set by the differ's Normalize.
		normalized string
	}

	// UniqueConstraint marks a live unique index that backs a UNIQUE
	// constraint, which is dropped with ALTER TABLE ... DROP CONSTRAINT.
	UniqueConstraint struct {
		schema.Attr
	}
	Disabled struct {
		schema.Attr
	}

	// FillFactor is an index's FILLFACTOR percentage, 1-99. Its absence is the
	// server default (0, equivalent to 100).
	FillFactor struct {
		schema.Attr
		Percent int
	}

	// RowGUID marks the table's ROWGUIDCOL column.
	RowGUID struct {
		schema.Attr
	}

	// QuotedIdentifierOff marks a module created under SET QUOTED_IDENTIFIER OFF.
	QuotedIdentifierOff struct {
		schema.Attr
	}
)

func fillFactorOf(attrs []schema.Attr) int {
	if f, ok := attrOf[*FillFactor](attrs); ok {
		return f.Percent
	}
	return 0
}

// CollationChanged reports an explicit collation change between two columns.
func CollationChanged(from, to *schema.Column) bool {
	c1, _ := attrOf[*schema.Collation](from.Attrs)
	c2, _ := attrOf[*schema.Collation](to.Attrs)
	return (c1 == nil) != (c2 == nil) || c1 != nil && c1.V != c2.V
}

// RowGUIDChanged reports a ROWGUIDCOL property change between two columns.
func RowGUIDChanged(from, to *schema.Column) bool {
	_, r1 := attrOf[*RowGUID](from.Attrs)
	_, r2 := attrOf[*RowGUID](to.Attrs)
	return r1 != r2
}

func quotedIdentifierOff(attrs []schema.Attr) bool {
	_, ok := attrOf[*QuotedIdentifierOff](attrs)
	return ok
}

func attrOf[T schema.Attr](attrs []schema.Attr) (T, bool) {
	for _, attr := range attrs {
		if t, ok := attr.(T); ok {
			return t, true
		}
	}
	var zero T
	return zero, false
}
