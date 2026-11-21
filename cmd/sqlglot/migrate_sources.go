package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/sqlglot-go/database/migrate/workflow"
)

// readBundleSources reads migrate plan's positional paths into bundle files,
// each named by the kind its content holds (workflow.ClassifyFiles). A file
// path must hold SQL or HCL. A directory contributes every file under it that
// does, relative to the directory and confined to it as --bundle is, skipping
// hidden entries; the files it skips are listed on report.
func readBundleSources(paths []string, report io.Writer) ([]workflow.File, error) {
	var bundle []workflow.File
	var skipped []string
	given := map[string]bool{}
	add := func(name, content string) error {
		if given[name] {
			return fmt.Errorf("source path %s is given twice", name)
		}
		given[name] = true
		bundle = append(bundle, workflow.File{Path: name, Content: content})
		return nil
	}
	for _, arg := range paths {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			if err := readSourceDir(arg, add, &skipped); err != nil {
				return nil, fmt.Errorf("read sources under %s: %w", arg, err)
			}
			continue
		}
		data, err := os.ReadFile(filepath.Clean(arg))
		if err != nil {
			return nil, err
		}
		if err := add(sourceName(arg), string(data)); err != nil {
			return nil, err
		}
	}
	if len(bundle) == 0 {
		return nil, fmt.Errorf("no SQL or HCL files in %s", strings.Join(paths, ", "))
	}
	if len(skipped) > 0 {
		if _, err := fmt.Fprintf(report, "skipped %d files that hold neither SQL nor HCL: %s\n", len(skipped), skippedSummary(skipped)); err != nil {
			return nil, err
		}
	}
	return workflow.ClassifyFiles(bundle)
}

// skippedSummary lists skipped paths, counting a file name met more than
// three times instead of listing each, so a scripts tree's metadata files do
// not bury the script that was skipped.
func skippedSummary(skipped []string) string {
	byName := map[string]int{}
	for _, file := range skipped {
		byName[filepath.Base(file)]++
	}
	var parts []string
	counted := map[string]bool{}
	for _, file := range skipped {
		name := filepath.Base(file)
		switch {
		case byName[name] <= 3:
			parts = append(parts, file)
		case !counted[name]:
			counted[name] = true
			parts = append(parts, fmt.Sprintf("%d named %s", byName[name], name))
		}
	}
	return strings.Join(parts, ", ")
}

// readSourceDir adds every SQL or HCL file under dir and records the files of
// neither kind on skipped.
func readSourceDir(dir string, add func(name, content string) error, skipped *[]string) (returnErr error) {
	opened, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, opened.Close()) }()
	root := opened.FS()
	return fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case name != "." && strings.HasPrefix(entry.Name(), "."):
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		case entry.IsDir():
			return nil
		}
		data, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		if _, ok := workflow.DetectContent(name, string(data)); !ok {
			*skipped = append(*skipped, filepath.Join(dir, name))
			return nil
		}
		return add(name, string(data))
	})
}

// sourceName is a file argument's bundle path: the path as given when it is
// local to the working directory, else its base name.
func sourceName(arg string) string {
	if filepath.IsLocal(arg) {
		return filepath.ToSlash(filepath.Clean(arg))
	}
	return filepath.Base(arg)
}
