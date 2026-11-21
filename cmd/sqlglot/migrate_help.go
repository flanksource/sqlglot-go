package main

import (
	_ "embed"
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
)

//go:embed help/migrate.md
var migrationHelp string

//go:embed help/migrate-plan.md
var migrationPlanHelp string

//go:embed help/migrate-review.md
var migrationReviewHelp string

//go:embed help/migrate-apply.md
var migrationApplyHelp string

func renderMigrationHelp(cmd *cobra.Command, _ []string) {
	noColor, err := cmd.Root().PersistentFlags().GetBool("no-color")
	if err != nil {
		cmd.PrintErrln(err)
		return
	}
	options := clicky.FormatOptions{NoColor: noColor}
	options.ResolveNoColor()
	doc := clicky.MustParseMarkdown(cmd.Long)
	render := doc.ANSI
	if options.NoColor {
		render = doc.String
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n%s", render(), cmd.UsageString()); err != nil {
		cmd.PrintErrln(err)
	}
}
