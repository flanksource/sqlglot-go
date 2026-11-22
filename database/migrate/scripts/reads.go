package scripts

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
	"github.com/flanksource/sqlglot-go/sqlglot/access"
)

// Read is a query answering what a statement of a script reads, so two runs of
// it tell whether the statement would read different values. It runs on its
// own: the constant variables it names are declared in it.
type Read struct {
	SQL     string       `json:"sql"`
	Objects []ObjectName `json:"objects"`
}

// Key identifies the read in the ledger.
func (r Read) Key() string { return Digest("read", r.SQL)[:32] }

// analyzeReads lists the reads of a script's statements, and the tables it reads
// in a statement with no query that runs on its own.
func analyzeReads(sql string) (reads []Read, untracked []ObjectName) {
	batches, err := sqlglot.SplitBatches(sql, "tsql")
	if err != nil {
		return nil, nil
	}
	for _, batch := range batches {
		file, err := access.ReadTSQL(batch)
		if err != nil || file.Script == nil {
			continue
		}
		r := readCollector{constants: batchConstants(batch)}
		r.nodes(file.Script.Body)
		reads, untracked = append(reads, r.reads...), append(untracked, r.untracked...)
	}
	slices.SortFunc(reads, func(a, b Read) int { return strings.Compare(a.SQL, b.SQL) })
	slices.Sort(untracked)
	return slices.CompactFunc(reads, func(a, b Read) bool { return a.SQL == b.SQL }), slices.Compact(untracked)
}

type readCollector struct {
	constants map[string]string
	reads     []Read
	untracked []ObjectName
}

func (r *readCollector) nodes(nodes []access.Node) {
	for _, node := range nodes {
		r.node(node)
		// Dynamic SQL sees none of the variables of the batch running it.
		dynamic := readCollector{}
		dynamic.nodes(node.Dynamic)
		r.reads, r.untracked = append(r.reads, dynamic.reads...), append(r.untracked, dynamic.untracked...)
		for _, nested := range [][]access.Node{node.Then, node.Else, node.Catch} {
			r.nodes(nested)
		}
	}
}

func (r *readCollector) node(node access.Node) {
	var objects []ObjectName
	for _, a := range node.Accesses {
		if a.Kind == access.Read {
			objects = append(objects, objectName(a.Object))
		}
	}
	if len(objects) == 0 {
		return
	}
	queries := nodeQueries(node)
	for i, query := range queries {
		declared, runs := r.standalone(query)
		if !runs {
			queries = nil
			break
		}
		queries[i] = declared
	}
	for _, query := range queries {
		r.reads = append(r.reads, Read{SQL: query, Objects: objects})
	}
	if len(queries) == 0 {
		r.untracked = append(r.untracked, objects...)
	}
}

// standalone is query with the constant variables it names declared; ok is
// false when its answer depends on anything but the tables it reads, or when
// running it has an effect: planning runs it.
func (r *readCollector) standalone(query string) (_ string, ok bool) {
	tokens, err := tokenWords(query)
	if err != nil || strings.Contains(tokens, "@@") || strings.Contains(tokens, "#") {
		return "", false
	}
	if effect.MatchString(tokens) {
		return "", false
	}
	for _, word := range strings.Fields(tokens) {
		if builtin, found := sqlglot.LookupBuiltin(word, "tsql"); found && builtin.Dependency != sqlglot.DependencyEnvironment {
			return "", false
		}
	}
	var declarations []string
	for _, match := range variable.FindAllStringSubmatch(tokens, -1) {
		declaration, constant := r.constants[match[1]]
		if !constant {
			return "", false
		}
		declarations = append(declarations, declaration)
	}
	slices.Sort(declarations)
	return strings.Join(append(slices.Compact(declarations), query), "\n"), true
}

// nodeQueries are the queries answering what node reads; none when it has no
// such form.
func nodeQueries(node access.Node) []string {
	if node.Kind == access.IfNode || node.Kind == access.WhileNode {
		return []string{"SELECT CASE WHEN " + node.Condition + " THEN 1 ELSE 0 END"}
	}
	statement, err := sqlglot.ParseOne(node.Text, "tsql")
	if err != nil || statement == nil || statement.Args["with_"] != nil && statement.Class != "Select" {
		return nil
	}
	var queries []*sqlglot.Expression
	switch statement.Class {
	case "Select":
		queries = []*sqlglot.Expression{selectRead(statement)}
	case "Insert":
		if source, _ := statement.Args["expression"].(*sqlglot.Expression); source != nil && source.Class == "Select" {
			queries = []*sqlglot.Expression{selectRead(source)}
		} else if source != nil && source.Class == "Values" {
			queries = outerSelects(source)
		}
	case "Set", "Declare":
		queries = outerSelects(statement)
	case "Delete":
		queries = []*sqlglot.Expression{deleteRead(statement)}
	case "Update":
		queries = []*sqlglot.Expression{updateRead(statement)}
	}
	var out []string
	for _, query := range queries {
		if query == nil {
			return nil
		}
		sql, err := sqlglot.Generate(query, "tsql")
		if err != nil {
			return nil
		}
		out = append(out, sql)
	}
	return out
}

// selectRead is what a SELECT reads: without the variables it assigns, and as
// the rows it matches when its projection, or which of them it takes, varies.
func selectRead(e *sqlglot.Expression) *sqlglot.Expression {
	e = e.Copy()
	if e.Args["into"] != nil {
		return nil
	}
	projections, _ := e.Args["expressions"].([]*sqlglot.Expression)
	exact := e.Args["limit"] == nil
	for i, projection := range projections {
		if assigned := projection.This(); slices.Contains([]string{"EQ", "PropertyEQ"}, projection.Class) && assigned != nil && assigned.Class == "Parameter" {
			value, _ := projection.Args["expression"].(*sqlglot.Expression)
			if value == nil {
				return nil
			}
			projections[i] = value
		}
		exact = exact && !volatile(projections[i])
	}
	if exact {
		e.Set("expressions", projections)
		return e
	}
	if e.Args["from_"] == nil || slices.ContainsFunc(projections, func(p *sqlglot.Expression) bool { return len(p.FindAll("Select")) > 0 }) ||
		e.Args["group"] != nil || e.Args["having"] != nil {
		return nil
	}
	return rowsRead(e.Args["from_"], e.Args["joins"], e.Args["where"], e.Args["with_"])
}

func rowsRead(from, joins, where, with any) *sqlglot.Expression {
	args := []sqlglot.Arg{{Key: "expressions", Value: []*sqlglot.Expression{sqlglot.New("Star")}}, {Key: "from_", Value: from}}
	for _, arg := range []sqlglot.Arg{{Key: "joins", Value: joins}, {Key: "where", Value: where}, {Key: "with_", Value: with}} {
		if !absent(arg.Value) {
			args = append(args, arg)
		}
	}
	return sqlglot.New("Select", args...)
}

func absent(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case *sqlglot.Expression:
		return v == nil
	case []*sqlglot.Expression:
		return len(v) == 0
	}
	return false
}

// volatile reports a call in e whose value changes between two runs, or a
// variable: neither says anything about the rows read.
func volatile(e *sqlglot.Expression) bool {
	found := false
	e.Walk(func(child *sqlglot.Expression) bool {
		builtin, ok := child.Builtin("tsql")
		found = found || ok && builtin.Dependency != sqlglot.DependencyEnvironment || child.Class == "Parameter"
		return !found
	})
	return found
}

// deleteRead is the rows a DELETE matches.
func deleteRead(e *sqlglot.Expression) *sqlglot.Expression {
	e = e.Copy()
	if !onlyArgs(e, "this", "where", "tables") {
		return nil
	}
	return rowsRead(sqlglot.New("From", sqlglot.Arg{Key: "this", Value: e.This()}), nil, e.Args["where"], nil)
}

// updateRead is the rows an UPDATE matches; a value it reads from elsewhere has
// no query of its own.
func updateRead(e *sqlglot.Expression) *sqlglot.Expression {
	e = e.Copy()
	sets, _ := e.Args["expressions"].([]*sqlglot.Expression)
	if !onlyArgs(e, "this", "expressions", "from_", "where") || slices.ContainsFunc(sets, func(set *sqlglot.Expression) bool { return len(set.FindAll("Select")) > 0 }) {
		return nil
	}
	if from, _ := e.Args["from_"].(*sqlglot.Expression); from != nil {
		return rowsRead(from, nil, e.Args["where"], nil)
	}
	return rowsRead(sqlglot.New("From", sqlglot.Arg{Key: "this", Value: e.This()}), nil, e.Args["where"], nil)
}

func onlyArgs(e *sqlglot.Expression, keys ...string) bool {
	for _, key := range e.Keys {
		if value := e.Args[key]; !slices.Contains(keys, key) && !absent(value) && value != false && value != "" {
			return false
		}
	}
	return true
}

// outerSelects are the queries in e that are in no other query of it.
func outerSelects(e *sqlglot.Expression) []*sqlglot.Expression {
	var out []*sqlglot.Expression
	e.Walk(func(child *sqlglot.Expression) bool {
		if child.Class == "Union" || child.Class == "Intersect" || child.Class == "Except" {
			out = append(out, child.Copy())
			return false
		}
		if child.Class == "Select" {
			out = append(out, selectRead(child))
			return false
		}
		return true
	})
	return out
}

var (
	variable   = regexp.MustCompile(`(?:^|[^@\w])@ ?([A-Z0-9_#$]+)`)
	assignment = regexp.MustCompile(`(?:^|(\S+) )@ ?([A-Z0-9_#$]+) (?:[-+*/%&|^] ?)?=`)
	output     = regexp.MustCompile(`@ ?([A-Z0-9_#$]+) (?:OUT|OUTPUT)(?: |$)`)
	fetched    = regexp.MustCompile(`(?:^| )(?:FETCH|RECEIVE|OUTPUT) .*?INTO @`)
	effect     = regexp.MustCompile(`(?:^| )(?:INSERT|UPDATE|DELETE|MERGE|TRUNCATE|EXEC|EXECUTE|INTO|NEXT VALUE FOR|OPENQUERY|OPENROWSET|OPENDATASOURCE)(?: |$)`)
	comparison = []string{"WHERE", "AND", "OR", "ON", "IF", "WHEN", "WHILE", "NOT", "HAVING", "("}
)

// batchConstants are the declarations of the variables of batch that keep the
// constant they are declared with, by upper-cased name.
func batchConstants(batch string) map[string]string {
	words, err := tokenWords(batch)
	if err != nil || fetched.MatchString(words) {
		return nil
	}
	parsed, err := sqlglot.ParseScript(batch, "tsql")
	if err != nil || parsed.Tree == nil || !parsed.Complete {
		return nil
	}
	assigned := map[string]bool{}
	for _, match := range assignment.FindAllStringSubmatch(words, -1) {
		if !slices.Contains(comparison, match[1]) {
			assigned[match[2]] = true
		}
	}
	for _, match := range output.FindAllStringSubmatch(words, -1) {
		assigned[match[1]] = true
	}
	constants, declared := map[string]string{}, map[string]bool{}
	for _, item := range parsed.Tree.FindAll("DeclareItem") {
		names, _ := item.Args["this"].([]*sqlglot.Expression)
		if name, ok := item.Args["this"].(*sqlglot.Expression); ok {
			names = []*sqlglot.Expression{name}
		}
		value, _ := item.Args["default"].(*sqlglot.Expression)
		kind, _ := item.Args["kind"].(*sqlglot.Expression)
		for _, name := range names {
			key := strings.ToUpper(strings.TrimPrefix(variableName(name), "@"))
			if key == "" || declared[key] || assigned[key] {
				declared[key] = true
				delete(constants, key)
				continue
			}
			declared[key] = true
			if value == nil || kind == nil || !constantExpression(value) {
				continue
			}
			typed, kindErr := sqlglot.Generate(kind, "tsql")
			literal, err := sqlglot.Generate(unwrapped(value.Copy()), "tsql")
			if err == nil && kindErr == nil {
				constants[key] = fmt.Sprintf("DECLARE @%s %s = %s;", key, typed, literal)
			}
		}
	}
	return constants
}

func variableName(e *sqlglot.Expression) string {
	if e.Class == "Parameter" && e.This() != nil {
		return e.This().Name()
	}
	return e.Name()
}

// tokenWords is sql as upper-cased tokens separated by one space, each string
// literal an empty one.
func tokenWords(sql string) (string, error) {
	tokenizer, err := sqlglot.NewTokenizer("tsql")
	if err != nil {
		return "", err
	}
	tokens, err := tokenizer.Tokenize(sql)
	if err != nil {
		return "", err
	}
	words := make([]string, len(tokens))
	for i, token := range tokens {
		words[i] = strings.ToUpper(token.Text)
		if token.Type == sqlglot.TokSTRING || token.Type == sqlglot.TokNATIONAL_STRING {
			words[i] = "''"
		}
	}
	return strings.Join(words, " "), nil
}
