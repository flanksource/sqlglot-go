package sqlinspect

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"testing"
)

func TestInspection(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Database inspection") }
