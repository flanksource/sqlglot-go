package workflow

import (
	"encoding/json"
	"io/fs"
	"time"

	"github.com/flanksource/sqlglot-go/database/migrate/csvdata"
	scriptplan "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	native "github.com/flanksource/sqlglot-go/migrate"
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
	Dialect    string
	Schema     string
	Source     Source
	Bundle     fs.FS
	Variables  []string
	Routines   string
	AllowDrops bool
	CSV        csvdata.Options
}

type Step struct {
	ID          string `json:"id"`
	SQL         string `json:"sql"`
	Destructive bool   `json:"destructive"`
}

type PlanResult struct {
	Dialect    string            `json:"dialect"`
	Schema     string            `json:"schema"`
	Artifact   json.RawMessage   `json:"artifact"`
	Hash       string            `json:"hash"`
	Acks       []string          `json:"acks"`
	Steps      []Step            `json:"steps"`
	Changes    []native.Change   `json:"changes"`
	Warnings   []string          `json:"warnings"`
	Applicable bool              `json:"applicable"`
	SQL        string            `json:"sql"`
	CSVPlan    *csvdata.Artifact `json:"csvPlan,omitempty"`
	// ScriptPlan is the plan file of a SQL Server script bundle.
	ScriptPlan *scriptplan.PlanFile `json:"scriptPlan,omitempty"`
	Original   json.RawMessage      `json:"-"`
}

// ApplyOptions applies an artifact as reviewed: its own hash guards its
// integrity, and Acks acknowledge its destructive steps.
type ApplyOptions struct {
	Dialect     string
	Schema      string
	Artifact    json.RawMessage
	Acks        []string
	LockTimeout time.Duration
	CSV         csvdata.Options
}
