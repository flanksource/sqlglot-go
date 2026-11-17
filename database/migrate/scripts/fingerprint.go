package scripts

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func SourceHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func Digest(fields ...string) string {
	h := sha256.New()
	for _, field := range fields {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func InputHash(inputs map[string]string) string {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	fields := []string{"script-inputs-v2"}
	for _, key := range keys {
		fields = append(fields, key, inputs[key])
	}
	return Digest(fields...)
}

type Scalar struct {
	Type      string `json:"type"`
	Length    int64  `json:"length"`
	Precision int64  `json:"precision"`
	Scale     int64  `json:"scale"`
	Null      bool   `json:"null"`
	Value     string `json:"value"`
}

func (s Scalar) Fingerprint(invocation string) (string, error) {
	switch strings.ToLower(s.Type) {
	case "varchar", "nvarchar", "char", "nchar":
	default:
		return "", fmt.Errorf("invocation %s: unsupported scalar type %q", invocation, s.Type)
	}
	if s.Null && s.Value != "" {
		return "", fmt.Errorf("invocation %s: NULL scalar carries a value", invocation)
	}
	return Digest("scalar-v2", invocation, strings.ToLower(s.Type), strconv.FormatInt(s.Length, 10),
		strconv.FormatInt(s.Precision, 10), strconv.FormatInt(s.Scale, 10), strconv.FormatBool(s.Null), s.Value), nil
}
