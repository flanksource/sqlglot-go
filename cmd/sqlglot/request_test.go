package main

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("content request limits", func() {
	DescribeTable("enforces the endpoint's upload limit before dispatch", func(path string, bytes int64, status int) {
		called := false
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+path, nil)
		request.ContentLength = bytes
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		guardUIRequests(next, uiHandlerOptions{}).ServeHTTP(response, request)
		Expect(response.Code).To(Equal(status))
		Expect(called).To(Equal(status == http.StatusOK))
	},
		Entry("rejects a large SQL tools request", "/api/v1/parse", int64(16*mib+1), http.StatusRequestEntityTooLarge),
		Entry("accepts a project bundle above the SQL tools limit", "/api/v1/migrate/plan", int64(36*mib), http.StatusOK),
		Entry("rejects uploads above the migration limit", "/api/v1/migrate/sources", int64(128*mib+1), http.StatusRequestEntityTooLarge),
	)
})
