package history

import (
	"encoding/json"
	"fmt"
)

func redactPayload(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("history payload is not valid JSON")
	}
	return redactJSON(data)
}

func redactJSON(data json.RawMessage) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err == nil && object != nil {
		changed := false
		for key, member := range object {
			var dsn string
			var next []byte
			var err error
			if (key == "target" || key == "reference" || key == "dsn") && json.Unmarshal(member, &dsn) == nil {
				next, err = json.Marshal(Redact(dsn))
			} else {
				next, err = redactJSON(member)
			}
			if err != nil {
				return nil, err
			}
			if string(next) != string(member) {
				object[key], changed = next, true
			}
		}
		if changed {
			return json.Marshal(object)
		}
		return data, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err == nil && rows != nil {
		changed := false
		for i, row := range rows {
			next, err := redactJSON(row)
			if err != nil {
				return nil, err
			}
			if string(next) != string(row) {
				rows[i], changed = next, true
			}
		}
		if changed {
			return json.Marshal(rows)
		}
	}
	return data, nil
}
