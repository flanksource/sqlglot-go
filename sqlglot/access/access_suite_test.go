package access_test

import (
	"io/fs"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/sqlglot-go/sqlglot/access"
)

func TestAccess(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SQL object access")
}

// edge is one access of a routine as these specs compare it: what it does to
// which object, the line of its statement and the guards it runs under.
type edge struct {
	Kind   access.Kind
	To     string
	Line   int
	Guards []string
}

// routineView is one routine: its name, where it starts, its parameters and
// what its body does.
type routineView struct {
	Name   string
	Line   int
	Params []string
	Edges  []edge
}

func edges(nodes []access.Node) []edge {
	out := []edge{}
	var visit func(nodes []access.Node, guards []string)
	visit = func(nodes []access.Node, guards []string) {
		for _, node := range nodes {
			for _, a := range node.Accesses {
				out = append(out, edge{Kind: a.Kind, To: a.Object.String(), Line: a.Line, Guards: append([]string{}, guards...)})
			}
			visit(node.Dynamic, guards)
			switch node.Kind {
			case access.IfNode:
				visit(node.Then, append(append([]string{}, guards...), node.Condition))
				visit(node.Else, append(append([]string{}, guards...), "!"+node.Condition))
			case access.WhileNode:
				visit(node.Then, append(append([]string{}, guards...), node.Condition))
			case access.TryNode:
				visit(node.Then, guards)
				visit(node.Catch, guards)
			}
		}
	}
	visit(nodes, nil)
	return out
}

func targets(nodes []access.Node) []string {
	out := []string{}
	for _, a := range access.Collect(nodes) {
		out = append(out, string(a.Kind)+" "+a.Object.String())
	}
	return out
}

func routineViews(file access.File) []routineView {
	views := []routineView{}
	for _, routine := range file.Routines {
		params := []string{}
		for _, param := range routine.Definition.Params {
			params = append(params, param.Name)
		}
		views = append(views, routineView{Name: routine.Object.String(), Line: routine.Line, Params: params, Edges: edges(routine.Body)})
	}
	return views
}

func readTSQL(src string) access.File {
	GinkgoHelper()
	file, err := access.ReadTSQL(src)
	Expect(err).NotTo(HaveOccurred())
	return file
}

func readFixture(name string) access.File {
	GinkgoHelper()
	src, err := fs.ReadFile(os.DirFS("testdata"), name)
	Expect(err).NotTo(HaveOccurred())
	return readTSQL(string(src))
}

// readOne reads a source holding one routine and returns it with the file's
// diagnostics.
func readOne(src string) (access.Routine, []access.Diagnostic) {
	GinkgoHelper()
	file := readTSQL(src)
	Expect(file.Routines).To(HaveLen(1))
	Expect(file.Script).To(BeNil())
	return file.Routines[0], file.Diagnostics
}
