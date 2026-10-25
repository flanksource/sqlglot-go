package connections_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/sqlglot-go/database/connections"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestConnections(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Connections") }

func writeConnection(dir, file, body string) {
	Expect(os.WriteFile(filepath.Join(dir, file), []byte(body), 0600)).To(Succeed())
}

var _ = Describe("connection catalog", func() {
	var dir string
	BeforeEach(func() { dir = GinkgoT().TempDir() })

	It("defaults the name to the file stem", func() {
		writeConnection(dir, "orders.yaml", "type: postgres\nurl: postgres://db.example/orders\n")
		catalog, err := connections.Load(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(catalog).To(HaveKey("orders"))
		Expect(catalog["orders"].Name).To(Equal("orders"))
		Expect(catalog["orders"].Type).To(Equal("postgres"))
	})

	DescribeTable("rejects invalid connection files",
		func(files map[string]string, message string) {
			for file, body := range files {
				writeConnection(dir, file, body)
			}
			_, err := connections.Load(dir)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("name differs from the file stem", map[string]string{"orders.yaml": "name: billing\ntype: postgres\nurl: postgres://db.example/orders\n"}, `name "billing" does not match file stem "orders"`),
		Entry("duplicate name across extensions", map[string]string{"orders.yaml": "type: postgres\nurl: postgres://a\n", "orders.yml": "type: postgres\nurl: postgres://b\n"}, `duplicate connection "orders"`),
		Entry("unknown field", map[string]string{"orders.yaml": "type: postgres\nurl: postgres://a\nhost: db.example\n"}, `unknown field "host"`),
		Entry("non-SQL type", map[string]string{"cache.yaml": "type: redis\nurl: redis://cache.example\n"}, `connection "cache" has type "redis"`),
	)

	It("fails when the directory does not exist", func() {
		_, err := connections.Load(filepath.Join(dir, "missing"))
		Expect(err).To(MatchError(ContainSubstring("missing")))
	})
})

var _ = Describe("connection resolution", func() {
	const (
		username = "app@corp"
		password = "p@ss/word"
	)
	var dir string
	BeforeEach(func() { dir = GinkgoT().TempDir() })

	It("templates escaped credentials into the URL", func() {
		writeConnection(dir, "orders.yaml", "type: sql_server\nurl: sqlserver://$(username):$(password)@db.example?database=orders\nusername: "+username+"\npassword: "+password+"\n")
		dsn, err := connections.Resolve(context.Background(), connections.ResolveOptions{Dir: dir, Reference: "connection://orders"})
		Expect(err).NotTo(HaveOccurred())
		Expect(dsn).To(Equal("sqlserver://app%40corp:p%40ss%2Fword@db.example?database=orders"))
	})

	It("reads secret references from the connection namespace", func() {
		writeConnection(dir, "orders.yaml", `namespace: tenant-x
type: sql_server
url: sqlserver://$(username):$(password)@{{.properties.host}}?database={{.properties.database}}
username: secret://db/username
password: secret://db/password
properties:
  host: secret://db/host
  database: secret://db/database
`)
		clientset := fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-x", Name: "db"},
			Data: map[string][]byte{
				"username": []byte(username),
				"password": []byte(password),
				"host":     []byte("sql.tenant-x.example"),
				"database": []byte("orders"),
			},
		})
		client := dutyKubernetes.NewKubeClient(logger.GetLogger("k8s"), clientset, nil)
		dsn, err := connections.Resolve(context.Background(), connections.ResolveOptions{Dir: dir, Reference: "connection://tenant-x/orders", Kubernetes: client})
		Expect(err).NotTo(HaveOccurred())
		Expect(dsn).To(Equal("sqlserver://app%40corp:p%40ss%2Fword@sql.tenant-x.example?database=orders"))
	})

	It("rejects credentials the URL does not reference", func() {
		writeConnection(dir, "orders.yaml", "type: postgres\nurl: postgres://db.example/orders\nusername: app\n")
		_, err := connections.Resolve(context.Background(), connections.ResolveOptions{Dir: dir, Reference: "connection://orders"})
		Expect(err).To(MatchError(ContainSubstring("$(username)")))
	})

	It("names an unknown connection", func() {
		writeConnection(dir, "orders.yaml", "type: postgres\nurl: postgres://db.example/orders\n")
		_, err := connections.Resolve(context.Background(), connections.ResolveOptions{Dir: dir, Reference: "connection://missing"})
		Expect(err).To(MatchError(ContainSubstring(`"missing"`)))
	})

	It("rejects a namespace that does not match the connection", func() {
		writeConnection(dir, "orders.yaml", "namespace: tenant-x\ntype: postgres\nurl: postgres://db.example/orders\n")
		_, err := connections.Resolve(context.Background(), connections.ResolveOptions{Dir: dir, Reference: "connection://tenant-y/orders"})
		Expect(err).To(MatchError(ContainSubstring("tenant-y")))
	})
})
