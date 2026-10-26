// Package review plans and applies hash-bound, transactional schema migrations.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/flanksource/sqlglot-go/migrate"
)

type Snapshot struct {
	Dialect string `json:"dialect"`
	Schema  string `json:"schema"`
	HCL     string `json:"hcl"`
}

type Step struct {
	ID          string `json:"id"`
	SQL         string `json:"sql"`
	Comment     string `json:"comment,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
	// Changes are the IDs of the described changes this statement implements.
	Changes []string `json:"changes,omitempty"`
}

type Artifact struct {
	Version  int      `json:"version"`
	Baseline Snapshot `json:"baseline"`
	Desired  Snapshot `json:"desired"`
	// Changes lists every change between baseline and desired, selected or not.
	Changes []migrate.Change `json:"changes,omitempty"`
	// Selection limits Steps to these change IDs; empty means every change.
	Selection []string `json:"selection,omitempty"`
	Steps     []Step   `json:"steps"`
	Advisory  []string `json:"advisory,omitempty"`
	Hash      string   `json:"hash"`
}

type Options struct {
	Dialect         string
	Schema          string
	DesiredHCL      string
	Advisory        []string
	PreserveOmitted bool
	// Selection limits the plan to these change IDs; empty plans every change.
	Selection []string
	// Baseline plans from this reviewed snapshot instead of inspecting the database.
	Baseline *Snapshot
}

type Request struct {
	Artifact         Artifact
	Hash             string
	Acknowledgements []string
	LockTimeout      time.Duration
}

type Outcome string

const (
	Applied        Outcome = "applied"
	AlreadyApplied Outcome = "already-applied"
	Failed         Outcome = "failed"
	CommitUnknown  Outcome = "commit-unknown"
)

type Result struct {
	Hash    string  `json:"hash"`
	Outcome Outcome `json:"outcome"`
	Steps   int     `json:"steps"`
}

func (a Artifact) Acknowledgements() []string {
	var ids []string
	for _, step := range a.Steps {
		if step.Destructive {
			ids = append(ids, step.ID)
		}
	}
	return ids
}

func (a Artifact) digest() (string, error) {
	a.Hash = ""
	data, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validate(request Request) error {
	a := request.Artifact
	hash, err := a.digest()
	if err != nil {
		return err
	}
	if request.Hash == "" || a.Hash != request.Hash || hash != request.Hash {
		return fmt.Errorf("reviewed artifact hash mismatch")
	}
	if a.Version != 2 {
		return fmt.Errorf("unsupported schema artifact version %d", a.Version)
	}
	if a.Baseline.Dialect != a.Desired.Dialect || a.Baseline.Schema != a.Desired.Schema {
		return fmt.Errorf("artifact baseline and desired scope differ")
	}
	if len(a.Advisory) > 0 {
		return fmt.Errorf("advisory preview cannot be applied: %v", a.Advisory)
	}
	wanted := a.Acknowledgements()
	got := slices.Clone(request.Acknowledgements)
	slices.Sort(wanted)
	slices.Sort(got)
	if !slices.Equal(wanted, got) {
		return fmt.Errorf("destructive acknowledgement mismatch: expected %v, received %v", wanted, got)
	}
	if request.LockTimeout < 0 || request.LockTimeout > 24*time.Hour {
		return fmt.Errorf("lock timeout must be between zero and 24h")
	}
	return nil
}
