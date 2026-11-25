package workflow

import (
	"encoding/json"
	"io/fs"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/csvdata"
	scriptplan "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	"github.com/flanksource/sqlglot-go/migrate"
	"github.com/flanksource/sqlglot-go/migrate/engine"
)

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Source struct {
	Kind    string `json:"kind"`
	Content string `json:"content,omitempty"`
	Files   []File `json:"files,omitempty"`
}

type Options struct {
	Dialect string
	Schema  string
	Source  Source
	Bundle  fs.FS
	Project *Project
	// Labels select the project sources to plan (see Project.Select); they replace
	// the select of .sqlglot-go.yaml.
	Labels     []string
	Variables  []string
	Routines   string
	AllowDrops bool
	CSV        csvdata.Options
}

// Step is one statement of the plan, in execution order.
type Step struct {
	ID          string `json:"id"`
	SQL         string `json:"sql"`
	Destructive bool   `json:"destructive"`
	// Change is the planned change the statement implements, Unit the unit that
	// runs it and Phase its place in that change (drop, alter or create).
	Change           string `json:"change,omitempty"`
	Unit             string `json:"unit,omitempty"`
	Phase            string `json:"phase,omitempty"`
	NonTransactional bool   `json:"nonTransactional,omitempty"`
}

// Change is a planned change with the rebuilds it forces: the dependents (an
// index, key, check, default, statistic or view) dropped and re-created
// around it, each named by its own change with Cause set to this one.
type Change struct {
	migrate.Change
	Rebuilds []migrate.Change `json:"rebuilds,omitempty"`
}

// Unit is one transaction of the plan, with the data checks it runs first.
type Unit struct {
	ID            string                `json:"id"`
	Changes       []string              `json:"changes"`
	Objects       []migrate.Name        `json:"objects"`
	Transactional bool                  `json:"transactional"`
	Preconditions []engine.Precondition `json:"preconditions,omitempty"`
	// Steps are the IDs of the statements the unit runs, in order.
	Steps []string `json:"steps"`
}

type PlanResult struct {
	Dialect  string          `json:"dialect"`
	Schema   string          `json:"schema"`
	Artifact json.RawMessage `json:"artifact"`
	Hash     string          `json:"hash"`
	// Acks are the IDs of the destructive changes the plan carries, each of which apply must acknowledge.
	Acks     []string `json:"acks"`
	Steps    []Step   `json:"steps"`
	Changes  []Change `json:"changes"`
	Warnings []string `json:"warnings"`
	// Applicable is false for an advisory plan, which can be reviewed but not applied.
	Applicable bool `json:"applicable"`
	// Advisory lists why the plan is not applicable. A saved plan file must
	// carry it, or re-importing the file would make the plan applicable.
	Advisory []string        `json:"advisory,omitempty"`
	SQL      string          `json:"sql"`
	Sources  []ProjectSource `json:"sources,omitempty"`
	// Scope, Units, Preserved and Refusals describe a schema plan: what it manages,
	// how it is grouped into transactions, the drops its declared scope leaves
	// alone, and the objects inspection refused that no change touches.
	Scope     *engine.Scope     `json:"scope,omitempty"`
	Units     []Unit            `json:"units,omitempty"`
	Preserved []migrate.Change  `json:"preserved,omitempty"`
	Refusals  []engine.Refusal  `json:"refusals,omitempty"`
	CSVPlan   *csvdata.Artifact `json:"csvPlan,omitempty"`
	// ScriptPlan is the plan file of a SQL Server script bundle.
	ScriptPlan *scriptplan.PlanFile `json:"scriptPlan,omitempty"`
	Original   json.RawMessage      `json:"-"`
}

// ApplyOptions applies an artifact as reviewed: its own hash guards its
// integrity, and Acks acknowledge its destructive changes.
type ApplyOptions struct {
	Dialect     string
	Schema      string
	Artifact    json.RawMessage
	Acks        []string
	LockTimeout time.Duration
	CSV         csvdata.Options
}

// ApplyResult is the outcome of applying a schema artifact: Steps counts the
// statements that ran, Units reports each unit's own outcome.
type ApplyResult struct {
	Hash    string              `json:"hash"`
	Outcome engine.Outcome      `json:"outcome"`
	Steps   int                 `json:"steps"`
	Units   []engine.UnitResult `json:"units,omitempty"`
}
