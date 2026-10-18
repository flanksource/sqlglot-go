package main

import (
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

	parse := clicky.AddNamedCommand("parse", root, InputOptions{}, func(opts InputOptions) (ParseResult, error) {
		return parseSQL(opts, stdin)
	})
	parse.Short = "Parse SQL and show formatted SQL; JSON includes the expression tree"
	validate := clicky.AddNamedCommand("validate", root, InputOptions{}, func(opts InputOptions) (ValidationResult, error) {
		return validateSQL(opts, stdin)
	})
	validate.Short = "Check whether SQL parses as one query"
	format := clicky.AddNamedCommand("format", root, InputOptions{}, func(opts InputOptions) (string, error) {
		return formatSQL(opts, stdin)
	})
	format.Short = "Format SQL while preserving its tokens"
	tokens := clicky.AddNamedCommand("tokens", root, InputOptions{}, func(opts InputOptions) ([]TokenResult, error) {
		return tokenizeSQL(opts, stdin)
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
