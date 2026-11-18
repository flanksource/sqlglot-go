package csvdata

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

type cell struct {
	text   string
	quoted bool
}

func readRecords(body []byte, delimiter rune) ([][]cell, error) {
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("CSV must be UTF-8")
	}
	lines := []int{0}
	for index, value := range body {
		if value == '\n' {
			lines = append(lines, index+1)
		}
	}
	reader := csv.NewReader(bytes.NewReader(body))
	reader.Comma = delimiter
	var records [][]cell
	for {
		fields, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var invalid *csv.ParseError
			if errors.As(err, &invalid) {
				return nil, fmt.Errorf("CSV line %d column %d: %w", invalid.Line, invalid.Column, invalid.Err)
			}
			return nil, err
		}
		record := make([]cell, len(fields))
		for index, value := range fields {
			line, column := reader.FieldPos(index)
			offset := lines[line-1] + column - 1
			record[index] = cell{text: value, quoted: offset < len(body) && body[offset] == '"'}
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("missing CSV header")
	}
	return records, nil
}

func (s *Settings) merge(next Settings) {
	if next.Key != nil {
		s.Key = next.Key
	}
	if next.Delimiter != "" {
		s.Delimiter = next.Delimiter
	}
	if next.IgnoreColumns != nil {
		s.IgnoreColumns = next.IgnoreColumns
	}
	if next.Phase != "" {
		s.Phase = next.Phase
	}
	if next.Delete != "" {
		s.Delete = next.Delete
	}
	s.Values.merge(next.Values)
}

func (s *ValueSettings) merge(next ValueSettings) {
	if next.Blank != "" {
		s.Blank = next.Blank
	}
	if next.NullTokens != nil {
		s.NullTokens = next.NullTokens
	}
	if next.NullTokenCase != "" {
		s.NullTokenCase = next.NullTokenCase
	}
	for name, value := range next.Columns {
		if s.Columns == nil {
			s.Columns = make(map[string]ValueSettings)
		}
		key := strings.ToLower(name)
		column := s.Columns[key]
		column.merge(value)
		s.Columns[key] = column
	}
}

func (s Settings) validate() error {
	delimiter, size := utf8.DecodeRuneInString(s.Delimiter)
	if size != len(s.Delimiter) || strings.ContainsRune("\r\n\x00\"", delimiter) || delimiter == utf8.RuneError {
		return fmt.Errorf("delimiter must be one valid CSV separator")
	}
	if !contains([]string{"disabled", "explicit", "snapshot"}, s.Delete) {
		return fmt.Errorf("invalid delete policy %q", s.Delete)
	}
	if !contains([]string{"pre-rules", "post-rules"}, s.Phase) {
		return fmt.Errorf("invalid phase %q", s.Phase)
	}
	for _, name := range s.IgnoreColumns {
		if strings.EqualFold(name, "$delete") {
			return fmt.Errorf("$delete cannot be ignored")
		}
	}
	return s.Values.validate()
}

func (s ValueSettings) validate() error {
	if s.Blank != "" && !contains([]string{"empty-string", "sql-null", "error"}, s.Blank) {
		return fmt.Errorf("invalid blank policy %q", s.Blank)
	}
	if s.NullTokenCase != "" && !contains([]string{"sensitive", "insensitive"}, s.NullTokenCase) {
		return fmt.Errorf("invalid null token case %q", s.NullTokenCase)
	}
	if contains(s.NullTokens, "") {
		return fmt.Errorf("empty null token must use blank policy")
	}
	for name, column := range s.Columns {
		if len(column.Columns) != 0 {
			return fmt.Errorf("%s: column value settings cannot be nested", name)
		}
		if err := column.validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (s ValueSettings) decode(field cell) (any, error) {
	if field.quoted {
		return field.text, nil
	}
	if field.text == "" {
		switch s.Blank {
		case "sql-null":
			return nil, nil
		case "error":
			return nil, fmt.Errorf("unquoted empty value")
		default:
			return "", nil
		}
	}
	for _, token := range s.NullTokens {
		if token == field.text || (s.NullTokenCase == "insensitive" && strings.EqualFold(token, field.text)) {
			return nil, nil
		}
	}
	return field.text, nil
}
