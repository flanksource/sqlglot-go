package migrate

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
)

func TestApplyValidatesInputsBeforeConnecting(t *testing.T) {
	assert.EqualError(t, Apply(t.Context(), "", fstest.MapFS{}), "connection string is empty")
	assert.EqualError(t, Apply(t.Context(), "postgres://unused", nil), "schema filesystem is nil")
}
