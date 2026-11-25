//go:build project_corpus

package workflow

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path"
	"strings"

	backend "github.com/flanksource/sqlglot-go/database/migrate/scripts/sqlserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = It("maps the OIPA v2 checkout without changing SQL payload hashes", func() {
	root, err := os.OpenRoot(os.Getenv("SQLGLOT_PROJECT_CORPUS"))
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(root.Close)
	fsys := root.FS()
	project, err := LoadProject(fsys)
	Expect(err).NotTo(HaveOccurred())
	Expect(project).NotTo(BeNil())
	descriptors, err := fs.Glob(fsys, "scripts/*/data.xml")
	Expect(err).NotTo(HaveOccurred())
	want := map[string]string{}
	for _, descriptor := range descriptors {
		folder := path.Base(path.Dir(descriptor))
		switch {
		case folder == "PostDeployment_QA_investmentScaleUpdate_InvestmentProduct":
		case strings.HasPrefix(folder, "PreDeployment_"):
			want[descriptor] = "pre"
		case strings.HasPrefix(folder, "PostDeployment_"):
			want[descriptor] = "post"
		}
	}
	Expect(want).NotTo(BeEmpty())
	Expect(project.Files()).To(HaveLen(len(want)))
	var pre, post, country int
	var batchProblems, emptyScripts []string
	for _, file := range project.Files() {
		Expect(file.Phase).To(Equal(want[file.Path]), file.Path)
		body, err := fs.ReadFile(fsys, path.Join(path.Dir(file.Path), "SQLData.xml"))
		Expect(err).NotTo(HaveOccurred(), file.Path)
		Expect(sha256.Sum256([]byte(file.Content))).To(Equal(sha256.Sum256(body)), file.Path)
		Expect(strings.TrimSpace(file.Content)).NotTo(BeEmpty(), file.Path)
		batches, batchErr := backend.Batches(file.Path, file.Content)
		if batchErr != nil {
			batchProblems = append(batchProblems, batchErr.Error())
		} else if len(batches) == 0 {
			emptyScripts = append(emptyScripts, file.Path)
		}
		if file.Phase == "pre" {
			pre++
		} else {
			post++
		}
		if len(file.Labels["country"]) > 0 {
			country++
		}
	}
	AddReportEntry("mapped sources", map[string]int{"pre": pre, "post": post, "country-labelled": country})
	AddReportEntry("scripts without statements", emptyScripts)
	Expect(batchProblems).To(BeEmpty(), strings.Join(batchProblems, "\n"))
})

var _ = It("previews the uploaded OIPA project without sending unrelated files", func() {
	root, err := os.OpenRoot(os.Getenv("SQLGLOT_PROJECT_CORPUS"))
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(root.Close)
	fsys := root.FS()
	config, err := fs.ReadFile(fsys, ProjectConfigName)
	Expect(err).NotTo(HaveOccurred())
	var paths []string
	Expect(fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name != "." && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") {
			return fs.SkipDir
		}
		if !entry.IsDir() {
			paths = append(paths, name)
		}
		return nil
	})).To(Succeed())
	upload, err := ProjectUploadPaths(string(config), paths)
	Expect(err).NotTo(HaveOccurred())
	files := make([]File, 0, len(upload.Required))
	for _, name := range upload.Required {
		Expect(name == ProjectConfigName || strings.HasPrefix(name, "scripts/")).To(BeTrue(), name)
		body, err := fs.ReadFile(fsys, name)
		Expect(err).NotTo(HaveOccurred(), name)
		files = append(files, File{Path: name, Content: string(body)})
	}
	preview, err := PreviewProject(files)
	Expect(err).NotTo(HaveOccurred())
	descriptors, err := fs.Glob(fsys, "scripts/*/data.xml")
	Expect(err).NotTo(HaveOccurred())
	var deployment int
	for _, name := range descriptors {
		if strings.HasPrefix(name, "scripts/PreDeployment_") || strings.HasPrefix(name, "scripts/PostDeployment_") {
			deployment++
		}
	}
	AddReportEntry("project upload", preview)
	Expect(preview.Ignored).To(Equal(1))
	Expect(preview.Mapped).To(Equal(deployment - preview.Ignored))
})
