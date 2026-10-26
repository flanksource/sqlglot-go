package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/sqlglot-go/database/history"
	dbtarget "github.com/flanksource/sqlglot-go/database/target"
)

type RunOptions struct {
	Mode            string
	Target          dbtarget.OpenOptions
	Plan            Options
	Hash            string
	Acks            []string
	ArtifactContent json.RawMessage
	LockTimeout     string
}

type RunResult struct {
	Plan     *PlanResult `json:"plan,omitempty"`
	Mode     string      `json:"mode"`
	Hash     string      `json:"hash,omitempty"`
	Acks     []string    `json:"acks,omitempty"`
	Artifact any         `json:"artifact,omitempty"`
	Result   any         `json:"result,omitempty"`
}

func Execute(ctx context.Context, opts RunOptions, record *history.Record, t *task.Task) (payload RunResult, returnErr error) {
	payload.Mode, payload.Hash, payload.Acks = opts.Mode, opts.Hash, opts.Acks
	if opts.Mode != "plan" && opts.Mode != "apply" {
		return payload, fmt.Errorf("unknown migration mode %q", opts.Mode)
	}
	if opts.Mode == "apply" && len(opts.ArtifactContent) == 0 {
		return payload, fmt.Errorf("reviewed artifact is required for apply")
	}
	if opts.Mode == "plan" && (opts.Plan.Source.Kind == "artifact" || len(opts.ArtifactContent) > 0) {
		data := opts.ArtifactContent
		if opts.Plan.Source.Kind == "artifact" {
			data = []byte(opts.Plan.Source.Content)
		}
		plan, err := Review(data, opts.Target.Driver, opts.Target.Schema)
		if err != nil {
			return payload, err
		}
		record.Dialect, record.Schemas = plan.Dialect, []string{plan.Schema}
		payload.Plan, payload.Artifact = &plan, plan.Artifact
		return payload, nil
	}
	timeout := 5 * time.Second
	if opts.LockTimeout != "" {
		var err error
		timeout, err = time.ParseDuration(opts.LockTimeout)
		if err != nil {
			return payload, err
		}
	}
	if timeout <= 0 {
		return payload, fmt.Errorf("lock timeout must be positive")
	}
	apply := ApplyOptions{Hash: opts.Hash, Acks: opts.Acks, Artifact: opts.ArtifactContent, LockTimeout: timeout}
	if opts.Mode == "apply" && len(apply.Artifact) > 0 {
		if err := Validate(apply); err != nil {
			return payload, err
		}
	}
	t.SetDescription("Connecting to target")
	opts.Target.ReadOnly = opts.Mode != "apply"
	conn, err := dbtarget.Open(ctx, opts.Target)
	if err != nil {
		return payload, err
	}
	defer func() { returnErr = errors.Join(returnErr, conn.DB.Close()) }()
	record.Dialect, record.Schemas = conn.Dialect, []string{conn.Schema}
	planOpts := Options{Dialect: conn.Dialect, Schema: conn.Schema, Source: opts.Plan.Source, Variables: opts.Plan.Variables, Routines: opts.Plan.Routines, AllowDrops: opts.Plan.AllowDrops}
	planOpts.Bundle = opts.Plan.Bundle
	t.SetDescription("Reviewing migration")
	var plan PlanResult
	if len(apply.Artifact) > 0 {
		plan, err = Review(apply.Artifact, conn.Dialect, conn.Schema)
	} else {
		plan, err = Plan(ctx, conn.DB, planOpts)
	}
	if err != nil {
		return payload, err
	}
	payload.Plan, payload.Artifact = &plan, plan.Artifact
	if opts.Mode == "plan" {
		return payload, nil
	}
	t.SetDescription("Applying reviewed SQL")
	apply.Dialect, apply.Schema, apply.Artifact = conn.Dialect, conn.Schema, plan.Artifact
	payload.Result, err = Apply(ctx, conn.DB, apply)
	return payload, err
}
