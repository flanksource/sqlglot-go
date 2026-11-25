package sqlserver_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLServerPlanner(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Native SQL Server Migrations")
}
