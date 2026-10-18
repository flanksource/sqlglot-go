package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

type entry struct {
	File           string `json:"file"`
	Line           int    `json:"line"`
	Column         int    `json:"column"`
	Kind           string `json:"kind"`
	StatementKind  string `json:"statement_kind"`
	Label          string `json:"label"`
	Start          int    `json:"start"`
	End            int    `json:"end"`
	StatementStart int    `json:"statement_start"`
}

type group struct {
	Count int
	Files map[string]bool
	First entry
}

func main() {
	root := flag.String("root", "", "scripts directory containing PostDeployment*/SQLData.xml")
	out := flag.String("out", ".tmp", "output directory")
	exclude := flag.String("exclude", "PostDeployment_AsCode_AsCodeClientType", "directory to exclude")
	flag.Parse()
	if *root == "" {
		fail("--root is required")
	}
	paths, err := filepath.Glob(filepath.Join(*root, "PostDeployment*", "SQLData.xml"))
	if err != nil {
		fail("glob: %v", err)
	}
	if len(paths) == 0 {
		fail("no PostDeployment*/SQLData.xml files under %s", *root)
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		fail("create output directory: %v", err)
	}
	sourceRoot, err := os.OpenRoot(*root)
	if err != nil {
		fail("open source directory: %v", err)
	}
	outRoot, err := os.OpenRoot(*out)
	if err != nil {
		fail("open output directory: %v", err)
	}
	jsonPath := filepath.Join(*out, "postdeployment-unsupported.jsonl")
	jsonFile, err := outRoot.OpenFile("postdeployment-unsupported.jsonl", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		fail("create JSONL: %v", err)
	}
	writer := bufio.NewWriter(jsonFile)
	encoder := json.NewEncoder(writer)
	groups := map[string]*group{}
	files, clean, empty, gaps, diagnostics := 0, 0, 0, 0, 0
	for _, path := range paths {
		if filepath.Base(filepath.Dir(path)) == *exclude {
			continue
		}
		files++
		relative, err := filepath.Rel(*root, path)
		if err != nil {
			fail("relative path %s: %v", path, err)
		}
		data, err := sourceRoot.ReadFile(relative)
		if err != nil {
			fail("read %s: %v", path, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			empty++
			continue
		}
		result, err := sqlglot.ParseScript(string(data), "tsql")
		if err != nil {
			fail("parse %s: %v", path, err)
		}
		if len(result.Diagnostics) == 0 {
			clean++
		}
		if !result.Complete {
			gaps++
		}
		starts := []int{0}
		for i, r := range []rune(string(data)) {
			if r == '\n' {
				starts = append(starts, i+1)
			}
		}
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.StatementStart > diagnostic.Start {
				fail("invalid statement range in %s: start %d exceeds error %d", path, diagnostic.StatementStart, diagnostic.Start)
			}
			line := sort.Search(len(starts), func(i int) bool { return starts[i] > diagnostic.Start })
			record := entry{File: path, Line: line, Column: diagnostic.Start - starts[line-1] + 1,
				Kind: diagnostic.Kind, StatementKind: diagnostic.StatementKind, Label: diagnostic.Label,
				Start: diagnostic.Start, End: diagnostic.End, StatementStart: diagnostic.StatementStart}
			if err := encoder.Encode(record); err != nil {
				fail("write JSONL: %v", err)
			}
			key := diagnostic.StatementKind + " | " + diagnostic.Label
			if groups[key] == nil {
				groups[key] = &group{Files: map[string]bool{}, First: record}
			}
			groups[key].Count++
			groups[key].Files[path] = true
			diagnostics++
		}
	}
	if err := writer.Flush(); err != nil {
		fail("flush JSONL: %v", err)
	}
	if err := jsonFile.Close(); err != nil {
		fail("close JSONL: %v", err)
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "# PostDeployment SQLData.xml T-SQL parse inventory\n\nFiles scanned: %d. Nonempty files without diagnostics: %d. Empty files: %d. Diagnostics: %d. Incomplete scans: %d.\n\n", files, clean, empty, diagnostics, gaps)
	summary.WriteString("| Statement | Diagnostic | Occurrences | Files | Example |\n| --- | --- | ---: | ---: | --- |\n")
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if groups[keys[i]].Count != groups[keys[j]].Count {
			return groups[keys[i]].Count > groups[keys[j]].Count
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		g := groups[key]
		label := strings.ReplaceAll(g.First.Label, "|", "\\|")
		fmt.Fprintf(&summary, "| %s | %s | %d | %d | `%s:%d:%d` |\n", g.First.StatementKind, label,
			g.Count, len(g.Files), filepath.Base(filepath.Dir(g.First.File)), g.First.Line, g.First.Column)
	}
	summaryPath := filepath.Join(*out, "postdeployment-summary.md")
	if err := outRoot.WriteFile("postdeployment-summary.md", []byte(summary.String()), 0600); err != nil {
		fail("write summary: %v", err)
	}
	if err := outRoot.Close(); err != nil {
		fail("close output directory: %v", err)
	}
	if err := sourceRoot.Close(); err != nil {
		fail("close source directory: %v", err)
	}
	fmt.Printf("files=%d clean=%d empty=%d diagnostics=%d incomplete=%d\n%s\n%s\n", files, clean, empty, diagnostics, gaps, jsonPath, summaryPath)
	if gaps > 0 {
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
