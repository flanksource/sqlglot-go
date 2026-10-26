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

const header = "apiVersion: mission-control.flanksource.com/v1\nkind: Connection\n"

func writeConnection(dir, file, body string) {
	Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600)).To(Succeed())
}

var _ = Describe("connection resolution", func() {
	const (
		username = "app@corp"
		password = "p@ss/word"
	)
	var config, dir string
	BeforeEach(func() {
		config = GinkgoT().TempDir()
		GinkgoT().Setenv("XDG_CONFIG_HOME", config)
		dir = filepath.Join(config, "sqlglot-go", "connections")
	})
	resolve := func(reference string) (string, error) {
		return connections.Resolve(context.Background(), connections.ResolveOptions{Dir: dir, Reference: reference})
	}

	It("keeps sqlglot's own layer under the XDG config dir", func() {
		Expect(connections.DefaultDir()).To(Equal(dir))
	})

	It("templates escaped credentials into the URL", func() {
		writeConnection(dir, "orders.yaml", header+`metadata: {name: orders}
spec:
  type: sql_server
  url: {value: "sqlserver://$(username):$(password)@db.example?database=orders"}
  username: {value: "`+username+`"}
  password: {value: "`+password+`"}
`)
		dsn, err := resolve("connection://orders")
		Expect(err).NotTo(HaveOccurred())
		Expect(dsn).To(Equal("sqlserver://app%40corp:p%40ss%2Fword@db.example?database=orders"))
	})

	It("reads secret references from the connection namespace", func() {
		writeConnection(dir, "orders.yaml", header+`metadata: {name: orders, namespace: tenant-x}
spec:
  type: sql_server
  url: {value: "sqlserver://$(username):$(password)@{{.properties.host}}?database={{.properties.database}}"}
  username: {valueFrom: {secretKeyRef: {name: db, key: username}}}
  password: {valueFrom: {secretKeyRef: {name: db, key: password}}}
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

	It("lets sqlglot's own layer override the connections every tool shares", func() {
		writeConnection(filepath.Join(config, "flanksource", "connections"), "orders.yaml", header+"spec: {type: postgres, url: {value: 'postgres://shared/orders'}}\n")
		writeConnection(dir, "orders.yaml", header+"spec: {type: postgres, url: {value: 'postgres://own/orders'}}\n")
		writeConnection(filepath.Join(config, "flanksource", "connections"), "billing.yaml", header+"spec: {type: postgres, url: {value: 'postgres://shared/billing'}}\n")
		Expect(resolve("connection://orders")).To(Equal("postgres://own/orders"))
		Expect(resolve("connection://billing")).To(Equal("postgres://shared/billing"))
	})

	DescribeTable("rejects connections it cannot open",
		func(body, reference, message string) {
			writeConnection(dir, "orders.yaml", body)
			_, err := resolve(reference)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("credentials the URL does not reference", header+"spec: {type: postgres, url: {value: 'postgres://db.example/orders'}, username: {value: app}}\n", "connection://orders", "$(username)"),
		Entry("an unknown connection", header+"spec: {type: postgres, url: {value: 'postgres://db.example/orders'}}\n", "connection://missing", `"missing" not found`),
		Entry("a namespace the connection does not have", header+"metadata: {namespace: tenant-x}\nspec: {type: postgres, url: {value: 'postgres://db.example/orders'}}\n", "connection://tenant-y/orders", `"tenant-y/orders" not found`),
		Entry("a non-SQL type", header+"spec: {type: redis, url: {value: 'redis://cache.example'}}\n", "connection://orders", `has type "redis"`),
		Entry("a legacy flat connection file", "type: postgres\nurl: postgres://db.example/orders\n", "connection://orders", `"orders" cannot be used: connection `),
	)

	It("still resolves every other connection when one file is broken", func() {
		writeConnection(dir, "legacy.yaml", "type: postgres\nurl: postgres://db.example/legacy\n")
		writeConnection(dir, "orders.yaml", header+"spec: {type: postgres, url: {value: 'postgres://db.example/orders'}}\n")
		Expect(resolve("connection://orders")).To(Equal("postgres://db.example/orders"))
		_, err := resolve("connection://legacy")
		Expect(err).To(MatchError(And(ContainSubstring(`"legacy" cannot be used`), ContainSubstring("apiVersion: mission-control.flanksource.com/v1"))))
	})
})
