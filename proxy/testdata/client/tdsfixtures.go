package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
	"gopkg.in/yaml.v3"
)

// tdsFixtures regenerates proxy/tds/testdata/captures by running every scenario
// through `sqlglot proxy tds --capture` against a disposable database.
func tdsFixtures(o options) (result any, err error) {
	if o.connection == "" || o.upstream == "" || o.dir == "" || o.sqlglot == "" {
		return nil, errors.New("connection, upstream, dir and sqlglot are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	config, err := resolveUpstream(ctx, o)
	if err != nil {
		return nil, err
	}
	secrets := []string{config.Password, config.Host}
	defer func() {
		if err != nil && config.Password != "" {
			err = errors.New(strings.ReplaceAll(err.Error(), config.Password, "[redacted]"))
		}
	}()
	work, err := os.MkdirTemp(".tmp", "tds-fixtures-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	certificate := filepath.Join(work, "upstream.pem")
	if err := fetchCertificate(ctx, config, certificate); err != nil {
		return nil, err
	}
	admin, err := connect(config, func(q url.Values) {
		q.Set("encrypt", "true")
		q.Set(msdsn.ServerCertificate, certificate)
		q.Set(msdsn.Database, "master")
	})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, admin.Close()) }()
	var server, machine string
	if err := admin.QueryRowContext(ctx, "SELECT @@SERVERNAME, CAST(SERVERPROPERTY('MachineName') AS nvarchar(128))").Scan(&server, &machine); err != nil {
		return nil, fmt.Errorf("read server identity: %w", err)
	}
	database, drop, err := createDatabase(ctx, admin)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, drop()) }()
	captures := filepath.Join(work, "captures")
	if err := runScenarios(ctx, o, config, database, work, captures, certificate); err != nil {
		return nil, err
	}
	return publish(captures, o.dir, append(secrets, server, machine, database))
}

func resolveUpstream(ctx context.Context, o options) (msdsn.Config, error) {
	connections, err := store.Open("sqlglot-go")
	if err != nil {
		return msdsn.Config{}, err
	}
	model, err := connections.ResolveConnection(dbcontext.NewContext(ctx), o.connection)
	if err != nil {
		return msdsn.Config{}, fmt.Errorf("resolve %s: %w", o.connection, err)
	}
	if model == nil {
		return msdsn.Config{}, fmt.Errorf("%s is not configured", o.connection)
	}
	config, err := msdsn.Parse(model.URL)
	if err != nil {
		return msdsn.Config{}, fmt.Errorf("%s is not a SQL Server URL", o.connection)
	}
	if actual := net.JoinHostPort(config.Host, strconv.FormatUint(config.Port, 10)); actual != o.upstream {
		return msdsn.Config{}, fmt.Errorf("%s does not target %s", o.connection, o.upstream)
	}
	return config, nil
}

// connect opens one direct or proxied connection from the resolved credentials.
func connect(config msdsn.Config, configure func(url.Values)) (*sql.DB, error) {
	values := url.Values{}
	configure(values)
	host := net.JoinHostPort(config.Host, strconv.FormatUint(config.Port, 10))
	if address := values.Get("address"); address != "" {
		host = address
		values.Del("address")
	}
	endpoint := url.URL{Scheme: "sqlserver", Host: host, User: url.UserPassword(config.User, config.Password), RawQuery: values.Encode()}
	db, err := sql.Open("sqlserver", endpoint.String())
	if err != nil {
		return nil, errors.New("invalid SQL Server settings")
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

var errCertificate = errors.New("certificate received")

// fetchCertificate pins whatever the server presents and stops before login, so
// later connections are encrypted to that same server.
func fetchCertificate(ctx context.Context, config msdsn.Config, path string) error {
	probe, err := msdsn.Parse("sqlserver://example:example@" + net.JoinHostPort(config.Host, strconv.FormatUint(config.Port, 10)) + "?encrypt=true&trustservercertificate=true")
	if err != nil {
		return err
	}
	var der []byte
	probe.TLSConfig.VerifyConnection = func(state tls.ConnectionState) error {
		der = state.PeerCertificates[0].Raw
		return errCertificate
	}
	if _, err := mssql.NewConnectorConfig(probe).Connect(ctx); len(der) == 0 {
		return fmt.Errorf("fetch upstream certificate: %w", err)
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
}

func createDatabase(ctx context.Context, admin *sql.DB) (string, func() error, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, err
	}
	name := "sqlglot_tds_capture_" + hex.EncodeToString(random[:])
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE ["+name+"]"); err != nil {
		return "", nil, fmt.Errorf("create capture database: %w", err)
	}
	drop := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+name+"]"); err != nil {
			return fmt.Errorf("drop capture database %s: %w", name, err)
		}
		return nil
	}
	_, err := admin.ExecContext(ctx, "CREATE TABLE ["+name+"].dbo.tbl(a nvarchar(10), b nvarchar(10), c int); INSERT INTO ["+name+"].dbo.tbl VALUES (N'a', N'b', 1), (N'a', N'a', 2), (N'b', N'b', 3)")
	if err != nil {
		return "", nil, errors.Join(fmt.Errorf("create capture table: %w", err), drop())
	}
	return name, drop, nil
}

func runScenarios(ctx context.Context, o options, config msdsn.Config, database, work, captures, certificate string) (err error) {
	certs := filepath.Join(work, "certs")
	if _, err := certificates(certs); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, o.sqlglot, "proxy", "tds", "--upstream", o.upstream, "--capture", captures,
		"--upstream-tls", "require", "--upstream-cert", certificate,
		"--client-tls", "allow", "--tls-cert", filepath.Join(certs, "server.pem"), "--tls-key", filepath.Join(certs, "server-key.pem"))
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	var observed bytes.Buffer
	command.Stdout = &observed
	stderr, err := command.StderrPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	address, err := listenAddress(stderr)
	if err == nil {
		for _, scenario := range scenarios {
			if err = runScenario(ctx, config, scenario, address, database, certs); err != nil {
				err = fmt.Errorf("%s: %w\nproxy output:\n%s", scenario.name, err, observed.String())
				break
			}
		}
	}
	if signalErr := command.Process.Signal(os.Interrupt); signalErr != nil {
		err = errors.Join(err, signalErr)
	}
	return errors.Join(err, command.Wait())
}

func listenAddress(stderr io.Reader) (string, error) {
	line, err := bufio.NewReader(stderr).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("proxy did not start: %s: %w", line, err)
	}
	_, rest, found := strings.Cut(line, "listening on ")
	address, _, arrow := strings.Cut(rest, " -> ")
	if !found || !arrow {
		return "", fmt.Errorf("unexpected proxy output: %s", line)
	}
	return address, nil
}

func runScenario(ctx context.Context, config msdsn.Config, scenario scenario, address, database, certs string) error {
	db, err := connect(config, func(q url.Values) {
		q.Set("address", address)
		q.Set(msdsn.Database, database)
		q.Set(msdsn.PacketSize, strconv.Itoa(scenario.packetSize))
		q.Set("encrypt", "disable") // loopback only; the proxy encrypts the upstream leg
		if scenario.clientTLS {
			q.Set("encrypt", "true")
			q.Set(msdsn.ServerCertificate, filepath.Join(certs, "ca.pem"))
		}
	})
	if err != nil {
		return err
	}
	return errors.Join(scenario.run(ctx, db), db.Close())
}

// publish names the captures after their scenarios once no identity is found in them.
func publish(captures, dir string, identities []string) (any, error) {
	entries, err := os.ReadDir(captures)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(scenarios) {
		return nil, fmt.Errorf("expected one connection per scenario (%d), got %d", len(scenarios), len(entries))
	}
	fixtures := map[string][]byte{}
	counts := map[string]int{}
	root, err := os.OpenRoot(captures)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	for i, entry := range entries {
		data, err := root.ReadFile(entry.Name())
		if err != nil {
			return nil, err
		}
		if counts[scenarios[i].name], err = checkIdentities(data, identities); err != nil {
			return nil, fmt.Errorf("%s: %w", scenarios[i].name, err)
		}
		fixtures[scenarios[i].name] = data
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	for name, data := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o600); err != nil {
			return nil, err
		}
	}
	return map[string]any{"messages": counts}, nil
}

func checkIdentities(data []byte, identities []string) (int, error) {
	var messages []struct{ Packets []string }
	if err := yaml.Unmarshal(data, &messages); err != nil {
		return 0, err
	}
	for _, message := range messages {
		var payload []byte
		for _, encoded := range message.Packets {
			packet, err := hex.DecodeString(encoded)
			if err != nil {
				return 0, err
			}
			payload = append(payload, packet[8:]...)
		}
		for i, identity := range identities {
			if identity == "" {
				return 0, fmt.Errorf("identity %d is empty", i)
			}
			var wide []byte
			for _, unit := range utf16.Encode([]rune(identity)) {
				wide = binary.LittleEndian.AppendUint16(wide, unit)
			}
			obfuscated := slices.Clone(wide)
			for j, b := range obfuscated {
				obfuscated[j] = (b<<4 | b>>4) ^ 0xa5
			}
			for _, form := range [][]byte{[]byte(identity), wide, obfuscated} {
				if bytes.Contains(payload, form) {
					return 0, fmt.Errorf("capture contains identity %d", i)
				}
			}
		}
	}
	if len(messages) == 0 {
		return 0, errors.New("empty capture")
	}
	return len(messages), nil
}
