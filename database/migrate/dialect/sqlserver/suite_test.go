package sqlserver

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSQLServerDialect(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL Server schema dialect")
}
