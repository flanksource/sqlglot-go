package main

import (
	"bytes"
	"encoding/csv"
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/task"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func jsonCLICommand(cmd *cobra.Command) {
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		task.SetNoRender(true)
		flags := map[string]string{}
		cmd.Flags().Visit(func(flag *pflag.Flag) {
			if values, ok := flag.Value.(pflag.SliceValue); ok {
				var buffer bytes.Buffer
				writer := csv.NewWriter(&buffer)
				if err := writer.Write(values.GetSlice()); err != nil {
					panic(err)
				}
				writer.Flush()
				flags[flag.Name] = buffer.String()
			} else {
				flags[flag.Name] = flag.Value.String()
			}
		})
		value, err := entity.GetContextDataFunc(cmd)(cmd.Context(), flags, args)
		if err != nil {
			return err
		}
		switch result := value.(type) {
		case PlanOutput:
			if err := writeSummary(cmd, result.Pretty()); err != nil {
				return err
			}
			return writeDatabaseJSON(cmd, "", result.Original)
		case ApplyOutput:
			return writeDatabaseJSON(cmd, "", result.Value)
		case InspectionOutput:
			return writeDatabaseJSON(cmd, "", result.Value)
		default:
			return fmt.Errorf("unexpected command result %T", value)
		}
	}
}

// writeSummary prints a human summary to stderr, in color unless disabled.
func writeSummary(cmd *cobra.Command, text api.Text) error {
	noColor, err := cmd.Root().PersistentFlags().GetBool("no-color")
	if err != nil {
		return err
	}
	options := clicky.FormatOptions{NoColor: noColor}
	options.ResolveNoColor()
	rendered := text.ANSI()
	if options.NoColor {
		rendered = text.String()
	}
	_, err = fmt.Fprintln(cmd.ErrOrStderr(), rendered)
	return err
}
