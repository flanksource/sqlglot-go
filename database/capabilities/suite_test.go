package capabilities

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	_ "modernc.org/sqlite"
)

func TestCapabilities(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Database capabilities") }
