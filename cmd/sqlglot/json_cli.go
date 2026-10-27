package main

import (
	"bytes"
	"encoding/csv"
	"fmt"

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
		if cmd.Name() == "apply" && flags["hash"] == "" {
			return fmt.Errorf("reviewed --hash is required")
		}
		value, err := entity.GetContextDataFunc(cmd)(cmd.Context(), flags, args)
		if err != nil {
			return err
		}
		switch result := value.(type) {
		case PlanOutput:
			if flags["out"] != "" {
				return nil
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
