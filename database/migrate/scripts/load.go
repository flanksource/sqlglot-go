package scripts

import (
	"bufio"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

type LoadOptions struct {
	FS   fs.FS
	Dir  string
	Skip []string
}

func Load(opts LoadOptions) ([]Script, error) {
	if opts.FS == nil {
		return nil, fmt.Errorf("migration filesystem is nil")
	}
	if opts.Dir == "" {
		opts.Dir = "."
	}
	items := []Script{}
	err := fs.WalkDir(opts.FS, opts.Dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && slices.Contains(opts.Skip, name) {
			return fs.SkipDir
		}
		if entry.IsDir() || !strings.EqualFold(path.Ext(name), ".sql") {
			return nil
		}
		data, err := fs.ReadFile(opts.FS, name)
		if err != nil {
			return fmt.Errorf("read SQL %s: %w", name, err)
		}
		rel := name
		if opts.Dir != "." {
			rel = strings.TrimPrefix(name, opts.Dir+"/")
		}
		s, err := Parse(rel, string(data))
		if err != nil {
			return err
		}
		items = append(items, s)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load SQL migrations: %w", err)
	}
	return Ordered(items)
}

func Parse(name, content string) (Script, error) {
	if !fs.ValidPath(name) {
		return Script{}, fmt.Errorf("invalid SQL script path %q", name)
	}
	s := Script{ID: name, Path: name, Name: name, SQL: content, Phase: Post}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "--") {
			break
		}
		key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "--")), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if err := directive(&s, key, value); err != nil {
			return Script{}, fmt.Errorf("SQL script %q: %w", name, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return Script{}, fmt.Errorf("scan SQL script %q: %w", name, err)
	}
	return s, nil
}

func directive(s *Script, key, value string) error {
	switch key {
	case "phase":
		if Phase(value) != Pre && Phase(value) != Post {
			return fmt.Errorf("invalid phase %q", value)
		}
		s.Phase = Phase(value)
		s.HeaderPhase = s.Phase
	case "dependsOn":
		for _, dep := range strings.Split(value, ",") {
			dep = path.Clean(strings.TrimSpace(dep))
			if dep != "." && dep != "" {
				s.DependsOn = append(s.DependsOn, dep)
			}
		}
	case "runs":
		if value != "always" {
			return fmt.Errorf("invalid runs policy %q", value)
		}
		s.Always = true
	case "transaction":
		if value != "true" && value != "false" {
			return fmt.Errorf("invalid transaction value %q", value)
		}
		s.NonTransactional = value == "false"
	}
	return nil
}
