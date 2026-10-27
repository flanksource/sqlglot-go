package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api/filter/celfilter"
	"github.com/flanksource/clicky/rpc"
	"github.com/spf13/cobra"
)

type RenderOptions struct {
	Value      string `json:"value" flag:"value" help:"JSON result to render"`
	Format     string `json:"output-format" flag:"output-format" default:"json" help:"Download format"`
	Expression string `json:"expression,omitempty" flag:"expression" help:"CEL expression filtering result rows"`
}

type RenderOutput struct {
	Data        []byte `json:"data"`
	ContentType string `json:"contentType"`
	Format      string `json:"format"`
}

var resultFormats = map[string]string{
	"pretty": "text/plain", "json": "application/json", "ndjson": "application/x-ndjson",
	"toon": "text/plain", "yaml": "application/yaml", "csv": "text/csv",
	"html": "text/html", "markdown": "text/markdown", "llm": "text/markdown",
	"pdf": "application/pdf", "slack": "application/json",
}

func addRenderCommand(root *cobra.Command) {
	celfilter.Use()
	cmd := clicky.AddNamedCommandWithContext("render", root, RenderOptions{}, func(ctx context.Context, opts RenderOptions) (RenderOutput, error) {
		data := json.RawMessage(opts.Value)
		if request, ok := rpc.RequestFromContext(ctx); ok {
			var body struct {
				Value      json.RawMessage `json:"value"`
				Format     string          `json:"output-format"`
				Expression string          `json:"expression"`
			}
			if err := decodeRequest(request, &body); err != nil {
				return RenderOutput{}, err
			}
			data, opts.Format, opts.Expression = body.Value, body.Format, body.Expression
		}
		contentType, ok := resultFormats[opts.Format]
		if !ok {
			return RenderOutput{}, fmt.Errorf("unsupported output format %q", opts.Format)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return RenderOutput{}, err
		}
		if opts.Expression != "" {
			var rows []map[string]any
			if err := json.Unmarshal(data, &rows); err != nil {
				return RenderOutput{}, fmt.Errorf("result filters require object rows: %w", err)
			}
			value = rows
		}
		output, err := clicky.Format(value, clicky.FormatOptions{Format: opts.Format, Filter: opts.Expression, NoColor: true})
		return RenderOutput{Data: []byte(output), ContentType: contentType, Format: opts.Format}, err
	})
	cmd.Short = "Filter and render an existing result without running its operation"
	formats := clicky.AddNamedCommand("formats", cmd, struct{}{}, func(struct{}) (map[string]string, error) { return resultFormats, nil })
	formats.Short = "List supported result formats and MIME types"
}
