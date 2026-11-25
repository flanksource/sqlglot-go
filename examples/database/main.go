package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/sqlglot-go/database/migrate/dialect"
	"github.com/flanksource/sqlglot-go/migrate/engine"
	_ "modernc.org/sqlite"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (returnErr error) {
	if err := os.MkdirAll(".tmp", 0750); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(".tmp", "database-example-")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, os.RemoveAll(dir)) }()
	db, err := sql.Open("sqlite", filepath.Join(dir, "example.db"))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, db.Close()) }()
	ctx := context.Background()
	desired, err := dialect.DesiredFromHCL("sqlite", `schema "main" {}
table "items" {
 schema = schema.main
 column "id" { type = integer }
}`)
	if err != nil {
		return err
	}
	artifact, err := engine.PlanLive(ctx, db, engine.PlanLiveRequest{
		Dialect: "sqlite", Scope: engine.Scope{Mode: engine.ScopeComplete, Schemas: []string{"main"}}, Desired: desired,
	})
	if err != nil {
		return err
	}
	fmt.Println("reviewed hash:", artifact.Hash)
	for _, unit := range artifact.Units {
		for _, op := range unit.Ops {
			for _, statement := range op.Statements {
				fmt.Println(statement.SQL)
			}
		}
	}
	for range 2 {
		result, err := engine.Apply(ctx, db, engine.ApplyRequest{Artifact: artifact, Hash: artifact.Hash, Acknowledgements: engine.Acknowledgements(artifact)})
		if err != nil {
			return err
		}
		fmt.Println(result.Outcome)
	}
	return nil
}
