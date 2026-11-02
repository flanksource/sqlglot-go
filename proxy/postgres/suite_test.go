package postgres

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"testing"
)

func TestPostgres(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Postgres proxy") }
