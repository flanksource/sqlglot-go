package csvdata

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type compiledLayer struct {
	match    []*regexp.Regexp
	values   []string
	selected []string
}

func compileLayers(layers map[string]layer, filters map[string]string) (map[string]compiledLayer, error) {
	for name := range filters {
		if _, ok := layers[name]; !ok {
			return nil, fmt.Errorf("unknown layer %q", name)
		}
	}
	result := make(map[string]compiledLayer, len(layers))
	for name, cfg := range layers {
		if !identifier.MatchString(name) || len(cfg.Values) == 0 || len(cfg.Match) == 0 {
			return nil, fmt.Errorf("invalid layer %q", name)
		}
		if cfg.ValuePattern == "" {
			cfg.ValuePattern = "[A-Za-z0-9][A-Za-z0-9_-]*"
		}
		valuePattern, err := regexp.Compile("^(?:" + cfg.ValuePattern + ")$")
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", name, err)
		}
		current := compiledLayer{values: []string{"none"}}
		for _, value := range cfg.Values {
			if !valuePattern.MatchString(value) || strings.ContainsAny(value, "/\\,!*") || value == "" || contains(current.values, value) {
				return nil, fmt.Errorf("layer %s: invalid or duplicate value %q", name, value)
			}
			current.values = append(current.values, value)
		}
		for _, expr := range cfg.Match {
			pattern, err := layerPattern(expr, cfg.ValuePattern)
			if err != nil {
				return nil, fmt.Errorf("layer %s: %w", name, err)
			}
			current.match = append(current.match, pattern)
		}
		if cfg.DefaultFilter == "" {
			cfg.DefaultFilter = "none"
		}
		current.selected, err = filterValues(current.values, cfg.DefaultFilter)
		if err != nil {
			return nil, fmt.Errorf("layer %s default: %w", name, err)
		}
		if filter, ok := filters[name]; ok {
			current.selected, err = filterValues(current.values, filter)
			if err != nil {
				return nil, fmt.Errorf("layer %s: %w", name, err)
			}
		}
		result[name] = current
	}
	return result, nil
}

func layerPattern(expression, valuePattern string) (*regexp.Regexp, error) {
	if strings.Count(expression, "${value}") != 1 || strings.HasPrefix(expression, "/") || strings.Contains(expression, "\\") {
		return nil, fmt.Errorf("match requires one value capture in a relative path")
	}
	var pattern strings.Builder
	pattern.WriteString("^")
	for len(expression) > 0 {
		switch {
		case strings.HasPrefix(expression, "${value}"):
			pattern.WriteString("(?P<csvtag>" + valuePattern + ")")
			expression = expression[len("${value}"):]
		case strings.HasPrefix(expression, "**/"):
			pattern.WriteString("(?:[^/]+/)*")
			expression = expression[3:]
		case expression[0] == '*':
			pattern.WriteString("[^/]*")
			expression = expression[1:]
		case expression[0] == '?':
			pattern.WriteString("[^/]")
			expression = expression[1:]
		default:
			end := strings.IndexAny(expression, "*?$")
			if end == 0 {
				return nil, fmt.Errorf("unsupported match expression")
			}
			if end < 0 {
				end = len(expression)
			}
			pattern.WriteString(regexp.QuoteMeta(expression[:end]))
			expression = expression[end:]
		}
	}
	pattern.WriteString("$")
	return regexp.Compile(pattern.String())
}

func filterValues(values []string, expression string) ([]string, error) {
	include, exclude := map[string]bool{}, map[string]bool{}
	for _, term := range strings.Split(expression, ",") {
		value, negated := strings.CutPrefix(strings.TrimSpace(term), "!")
		if value == "*" && !negated {
			for _, all := range values {
				include[all] = true
			}
			continue
		}
		if !contains(values, value) {
			return nil, fmt.Errorf("unknown filter value %q", value)
		}
		if negated {
			exclude[value] = true
		} else {
			include[value] = true
		}
	}
	selected := []string{}
	for _, value := range values {
		if !exclude[value] && (len(include) == 0 || include[value]) {
			selected = append(selected, value)
		}
	}
	slices.Sort(selected)
	return selected, nil
}

func classify(path string, layers map[string]compiledLayer) (map[string]string, error) {
	tags := make(map[string]string, len(layers))
	for name, layer := range layers {
		tags[name] = "none"
		for _, pattern := range layer.match {
			match := pattern.FindStringSubmatch(path)
			if match == nil {
				continue
			}
			value := match[pattern.SubexpIndex("csvtag")]
			if value == "none" || !contains(layer.values, value) || strings.Contains(value, "/") {
				return nil, fmt.Errorf("unknown %s tag %q", name, value)
			}
			if tags[name] != "none" && tags[name] != value {
				return nil, fmt.Errorf("conflicting %s tags", name)
			}
			tags[name] = value
		}
	}
	return tags, nil
}
