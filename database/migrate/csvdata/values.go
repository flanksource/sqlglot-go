package csvdata

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func (c column) bind(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	text := value.(string)
	if text == "" && !contains([]string{"nvarchar", "varchar", "nchar", "char", "text", "ntext"}, c.Type) {
		return nil, fmt.Errorf("empty string is incompatible with %s", c.Type)
	}
	switch c.Type {
	case "uniqueidentifier":
		if len(text) != 36 {
			return nil, fmt.Errorf("expected a 36-character UUID")
		}
		id, err := uuid.Parse(text)
		if err != nil {
			return nil, fmt.Errorf("invalid UUID")
		}
		return id.String(), nil
	case "binary", "varbinary", "image":
		data, ok := strings.CutPrefix(text, "0x")
		if !ok {
			return nil, fmt.Errorf("binary value requires 0x-prefixed hex")
		}
		binary, err := hex.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("invalid hex value")
		}
		return binary, nil
	case "bit":
		switch strings.ToLower(text) {
		case "0", "false":
			return false, nil
		case "1", "true":
			return true, nil
		default:
			return nil, fmt.Errorf("expected a bit value")
		}
	default:
		return text, nil
	}
}
