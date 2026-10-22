package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/sqlglot-go/database/migrate/review"
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
	artifact, err := review.Build(ctx, db, review.Options{Dialect: "sqlite", Schema: "main", DesiredHCL: `schema "main" {}
table "items" {
 schema = schema.main
 column "id" { type = integer }
}`})
	if err != nil {
		return err
	}
	fmt.Println("reviewed hash:", artifact.Hash)
	for _, step := range artifact.Steps {
		fmt.Println(step.SQL)
	}
	for range 2 {
		result, err := review.Apply(ctx, db, review.Request{Artifact: artifact, Hash: artifact.Hash})
		if err != nil {
			return err
		}
		fmt.Println(result.Outcome)
	}
	return nil
}
