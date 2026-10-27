package main

import (
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
)

func addContentOpenAPI(spec *rpc.OpenAPISpec) {
	for path, methods := range spec.Paths {
		for method, op := range methods {
			if method != "post" {
				continue
			}
			if path != apiPrefix+"/migrate/plan" && path != apiPrefix+"/migrate/apply" && path != apiPrefix+"/migrate/review" && path != apiPrefix+"/render" {
				continue
			}
			if op.RequestBody == nil {
				op.RequestBody = &rpc.OpenAPIRequestBody{Content: map[string]rpc.OpenAPIMediaType{}}
			}
			media := op.RequestBody.Content["application/json"]
			if media.Schema == nil {
				media.Schema = &rpc.OpenAPISchema{Type: "object"}
			}
			if media.Schema.Properties == nil {
				media.Schema.Properties = map[string]*rpc.OpenAPISchema{}
			}
			if path == apiPrefix+"/render" {
				media.Schema.Properties["value"] = &rpc.OpenAPISchema{Description: "Existing JSON result to render; this never executes a database operation"}
			} else {
				media.Schema.Properties["source"] = rpc.SchemaForStruct(workflow.Source{})
				media.Schema.Properties["artifactContent"] = &rpc.OpenAPISchema{Type: "object", Description: "Unmodified reviewed artifact JSON"}
				for _, key := range []string{"bundle", "desired", "artifact", "out", "connections-dir"} {
					delete(media.Schema.Properties, key)
				}
			}
			op.RequestBody.Content["application/json"] = media
			methods[method] = op
		}
	}
}
