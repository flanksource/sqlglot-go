//go:build integration

// Package sqlservertest provisions private, temporary SQL Server fixtures for Ginkgo suites.
package sqlservertest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func OpenDisposable(ctx context.Context) *sql.DB {
	ginkgo.GinkgoHelper()
	endpoint, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "resolve the Docker context for disposable SQL Server")
	host := "127.0.0.1"
	if strings.HasPrefix(string(endpoint), "tcp://") {
		u, err := url.Parse(strings.TrimSpace(string(endpoint)))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", u.Hostname())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(addresses).NotTo(gomega.BeEmpty())
		gomega.Expect(addresses[0].IsPrivate() || addresses[0].IsLoopback()).To(gomega.BeTrue(), "SQL Server test host must be private")
		host = addresses[0].String()
	}
	password := "Test!" + rand.Text()
	name := "sqlglot-script-test-" + strings.ToLower(rand.Text())
	ginkgo.DeferCleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// #nosec G204 -- name is minted above from crypto/rand and passed as a single argument.
		if output, err := exec.CommandContext(cleanup, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
			gomega.Expect(string(output)).To(gomega.ContainSubstring("No such container"))
		}
	})
	// #nosec G204 -- the generated name and validated IP are individual arguments.
	command := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", "--name", name,
		"-e", "ACCEPT_EULA=Y", "-e", "MSSQL_PID=Developer", "-e", "MSSQL_SA_PASSWORD",
		"-p", host+"::1433", "mcr.microsoft.com/mssql/server:2025-latest")
	command.Env = append(os.Environ(), "MSSQL_SA_PASSWORD="+password)
	output, err := command.CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "start a fresh SQL Server container: %s", output)
	// #nosec G204 -- the generated name can only identify this fixture's container.
	port, err := exec.CommandContext(ctx, "docker", "port", name, "1433/tcp").Output()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, number, err := net.SplitHostPort(strings.TrimSpace(string(port)))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	dsn := url.URL{Scheme: "sqlserver", User: url.UserPassword("sa", password), Host: net.JoinHostPort(host, number), RawQuery: "database=master&encrypt=disable"}
	db, err := sql.Open("sqlserver", dsn.String())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ginkgo.DeferCleanup(db.Close)
	gomega.Eventually(func(ctx context.Context) error { return db.PingContext(ctx) }).
		WithContext(ctx).WithTimeout(2 * time.Minute).WithPolling(time.Second).Should(gomega.Succeed())
	_, err = db.ExecContext(ctx, "CREATE DATABASE script_dependencies")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	dsn.RawQuery = "database=script_dependencies&encrypt=disable"
	target, err := sql.Open("sqlserver", dsn.String())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ginkgo.DeferCleanup(target.Close)
	return target
}
