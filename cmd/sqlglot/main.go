package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
)

var version = "dev"

func newRootCommand(stdin io.Reader) *cobra.Command {
	root := &cobra.Command{
		Use:           "sqlglot",
		Short:         "Parse, validate, and format SQL",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			clicky.Flags.UseFlags()
		},
	}
	clicky.BindAllFlags(root.PersistentFlags(), "format")

	parse := clicky.AddNamedCommandWithContext("parse", root, InputOptions{}, func(ctx context.Context, opts InputOptions) (ParseResult, error) {
		input, err := requestSQL(ctx, opts)
		if err != nil {
			var zero ParseResult
			return zero, err
		}
		return parseSQL(input, stdin)
	})
	parse.Short = "Parse SQL and show formatted SQL; JSON includes the expression tree"
	validate := clicky.AddNamedCommandWithContext("validate", root, InputOptions{}, func(ctx context.Context, opts InputOptions) (ValidationResult, error) {
		input, err := requestSQL(ctx, opts)
		if err != nil {
			var zero ValidationResult
			return zero, err
		}
		return validateSQL(input, stdin)
	})
	validate.Short = "Check whether SQL parses as one query"
	format := clicky.AddNamedCommandWithContext("format", root, InputOptions{}, func(ctx context.Context, opts InputOptions) (string, error) {
		input, err := requestSQL(ctx, opts)
		if err != nil {
			var zero string
			return zero, err
		}
		return formatSQL(input, stdin)
	})
	format.Short = "Format SQL while preserving its tokens"
	tokens := clicky.AddNamedCommandWithContext("tokens", root, InputOptions{}, func(ctx context.Context, opts InputOptions) ([]TokenResult, error) {
		input, err := requestSQL(ctx, opts)
		if err != nil {
			var zero []TokenResult
			return zero, err
		}
		return tokenizeSQL(input, stdin)
	})
	tokens.Short = "List lexical tokens and their source positions"
	for _, command := range []*cobra.Command{parse, validate, format, tokens} {
		command.Args = cobra.MaximumNArgs(1)
	}
	dialects := clicky.AddNamedCommand("dialects", root, struct{}{}, func(_ struct{}) ([]DialectResult, error) {
		return availableDialects(), nil
	})
	dialects.Short = "List supported SQL dialects"
	versionCmd := clicky.AddNamedCommand("version", root, struct{}{}, func(_ struct{}) (string, error) {
		return version, nil
	})
	versionCmd.Short = "Print the CLI version"
	addDatabaseCommands(root)
	addHistoryCommands(root)
	addRenderCommand(root)
	addUICommand(root)
	return root
}

func main() {
	if err := newRootCommand(os.Stdin).Execute(); err != nil {
		var failure *ParseFailure
		if !errors.As(err, &failure) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
