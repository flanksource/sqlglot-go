package scripts

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

func (item *Pending) setValue(key string, value *string) {
	if item.State.Values == nil {
		item.State.Values = map[string]*string{}
	}
	item.State.Values[key] = value
}

func sameValue(a, b *string) bool {
	return a == b || (a != nil && b != nil && *a == *b)
}

// builtinDigest identifies the built-in values s and its prerequisites were
// planned with; empty when they track none.
func (s State) builtinDigest() string {
	var fields []string
	for _, key := range slices.Sorted(maps.Keys(s.Values)) {
		kind, _, _ := strings.Cut(key, ":")
		if kind != KindBuiltin && kind != "script" {
			continue
		}
		value := s.Values[key]
		fields = append(fields, key, strconv.FormatBool(value == nil))
		if value != nil {
			fields = append(fields, *value)
		}
	}
	if len(fields) == 0 {
		return ""
	}
	return Digest(fields...)
}

// FunctionInput is a function or built-in whose value or definition selects
// scripts of a plan.
type FunctionInput struct {
	Name string `json:"name"`
	// Builtin is a call of server built-ins, such as SUBSTRING(DB_NAME(), 5, 3).
	Builtin bool `json:"builtin,omitempty"`
	// Sampled is set when the plan observed a value, nil for NULL.
	Sampled bool    `json:"sampled,omitempty"`
	Value   *string `json:"value,omitempty"`
	// Writers are the scripts whose writes may change the value during apply.
	Writers []string      `json:"writers,omitempty"`
	Uses    []FunctionUse `json:"uses"`
}

// Display is the function as it is called.
func (f FunctionInput) Display() string {
	if f.Builtin {
		return f.Name
	}
	return f.Name + "()"
}

// FunctionUse is one script tracking a function, against what its last run
// recorded.
type FunctionUse struct {
	Script string `json:"script"`
	// Recorded is set when the last run recorded the value, Previous.
	Recorded          bool    `json:"recorded,omitempty"`
	Previous          *string `json:"previous,omitempty"`
	ValueChanged      bool    `json:"valueChanged,omitempty"`
	DefinitionChanged bool    `json:"definitionChanged,omitempty"`
}

// FunctionInputs lists the functions and built-ins the actions track, by name
// with built-ins first, against the states recorded of them.
func FunctionInputs(actions []Pending, recorded map[string]State) []FunctionInput {
	found := map[string]*FunctionInput{}
	for _, item := range actions {
		uses := map[string]*FunctionUse{}
		use := func(name string, builtin bool) (*FunctionInput, *FunctionUse) {
			if found[name] == nil {
				found[name] = &FunctionInput{Name: name, Builtin: builtin}
			}
			if uses[name] == nil {
				uses[name] = &FunctionUse{Script: item.Script.ID}
			}
			return found[name], uses[name]
		}
		old := recorded[item.Script.ID]
		for key, hash := range item.State.Inputs {
			kind, name, _ := strings.Cut(key, ":")
			before, known := old.Inputs[key]
			switch kind {
			case "definition":
				_, u := use(name, false)
				u.DefinitionChanged = known && before != hash
			case "result":
				_, u := use(name, false)
				u.ValueChanged = known && before != hash
			}
		}
		for key, value := range item.State.Values {
			kind, name, _ := strings.Cut(key, ":")
			if kind == "script" {
				continue
			}
			input, u := use(name, kind == KindBuiltin)
			input.Sampled, input.Value = true, value
			u.Previous, u.Recorded = old.Values[key]
			if kind == KindBuiltin {
				u.ValueChanged = u.Recorded && !sameValue(u.Previous, value)
			}
			for _, after := range item.AfterApply {
				if after.Key == key {
					input.Writers = append(input.Writers, after.Writers...)
				}
			}
		}
		for name, u := range uses {
			found[name].Uses = append(found[name].Uses, *u)
		}
	}
	out := make([]FunctionInput, 0, len(found))
	for _, input := range found {
		slices.Sort(input.Writers)
		input.Writers = slices.Compact(input.Writers)
		out = append(out, *input)
	}
	slices.SortFunc(out, func(a, b FunctionInput) int {
		if a.Builtin != b.Builtin {
			if a.Builtin {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
