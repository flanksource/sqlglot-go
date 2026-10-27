package main

import (
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("content request limits", func() {
	It("rejects oversized API bodies before dispatch", func() {
		called := false
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/migrate/plan", strings.NewReader(strings.Repeat("x", 16*1024*1024+1)))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		guardUIRequests(next, uiHandlerOptions{}).ServeHTTP(response, request)
		Expect(response.Code).To(Equal(http.StatusRequestEntityTooLarge))
		Expect(called).To(BeFalse())
	})
})
