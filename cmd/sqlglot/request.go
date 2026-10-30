package main

import (
	"context"
	"io"
	"net/http"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
)

func decodeRequest(request *http.Request, value any) error {
	data, err := io.ReadAll(io.LimitReader(request.Body, 16*1024*1024+1))
	if err != nil {
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_request", "%v", err)
	}
	if len(data) > 16*1024*1024 {
		return entity.NewStatusErrorf(http.StatusRequestEntityTooLarge, "request_too_large", "request exceeds 16 MiB")
	}
	if err := workflow.Decode(data, value); err != nil {
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_request", "%v", err)
	}
	return nil
}

func requestSQL(ctx context.Context, opts InputOptions) (InputOptions, error) {
	if request, ok := rpc.RequestFromContext(ctx); ok {
		if err := decodeRequest(request, &opts); err != nil {
			return InputOptions{}, err
		}
		if opts.File != "" {
			return InputOptions{}, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "upload SQL content instead of a server file path")
		}
		if opts.SQL == "" {
			return InputOptions{}, entity.NewStatusError(http.StatusBadRequest, "invalid_input", "SQL content is required")
		}
		return InputOptions{SQL: opts.SQL, Dialect: opts.Dialect}, nil
	}
	return opts, nil
}
