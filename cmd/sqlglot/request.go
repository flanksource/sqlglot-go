package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
)

const mib = 1024 * 1024

func requestBodyLimit(path string) int64 {
	if strings.HasPrefix(path, apiPrefix+"/migrate/") {
		return 128 * mib
	}
	return 16 * mib
}

func decodeRequest(request *http.Request, value any) error {
	limit := requestBodyLimit(request.URL.Path)
	data, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) || int64(len(data)) > limit {
		return entity.NewStatusErrorf(http.StatusRequestEntityTooLarge, "request_too_large", "request exceeds %d MiB", limit/mib)
	}
	if err != nil {
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_request", "%v", err)
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
