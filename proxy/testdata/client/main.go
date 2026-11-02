// The fixture client emits observations; expected behavior belongs in fixture CEL.
package main

import (
	"bufio"
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/flanksource/sqlglot-go/proxy/internal/testcert"
	_ "github.com/lib/pq"
	_ "github.com/microsoft/go-mssqldb"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type options struct {
	mode, driver, dsnEnv, address, sql, args, capture, marker, ca, dir string
	pid                                                                int
	padding                                                            int
}

func main() {
	var o options
	flag.StringVar(&o.mode, "mode", "query", "query, capture, certs, stop, closed")
	flag.StringVar(&o.driver, "driver", "postgres", "Database driver")
	flag.StringVar(&o.dsnEnv, "dsn-env", "FIXTURE_DSN", "Environment variable containing the upstream URL")
	flag.StringVar(&o.address, "address", "", "Proxy host:port; empty queries upstream directly")
	flag.StringVar(&o.sql, "sql", "", "SQL to execute")
	flag.StringVar(&o.args, "args", "[]", "JSON bind arguments")
	flag.StringVar(&o.capture, "capture", "", "NDJSON capture path")
	flag.StringVar(&o.marker, "marker", "", "SQL substring identifying the query")
	flag.StringVar(&o.ca, "ca", "", "Client TLS CA PEM file")
	flag.StringVar(&o.dir, "dir", "", "Directory for ephemeral certificates")
	flag.IntVar(&o.pid, "pid", 0, "Fixture-owned proxy PID")
	flag.IntVar(&o.padding, "padding", 0, "Unicode comment repetitions to force wire fragmentation")
	flag.Parse()
	value, err := run(o)
	if err != nil {
		value = map[string]any{"error": err.Error()}
	}
	if writeErr := json.NewEncoder(os.Stdout).Encode(value); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
}
func run(o options) (any, error) {
	switch o.mode {
	case "query", "active":
		return query(o)
	case "result":
		file, err := os.Open(o.capture)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		var result map[string]any
		err = json.NewDecoder(file).Decode(&result)
		return result, err
	case "capture":
		return capture(o)
	case "certs":
		return certificates(o.dir)
	case "stop":
		if o.pid < 1 {
			return nil, errors.New("pid must be positive")
		}
		process, err := os.FindProcess(o.pid)
		if err != nil {
			return nil, err
		}
		if err := process.Signal(os.Interrupt); err != nil {
			return nil, err
		}
		return map[string]any{"signalled": true}, nil
	case "closed":
		conn, err := net.DialTimeout("tcp", o.address, 100*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				return nil, err
			}
		}
		return map[string]any{"closed": err != nil}, nil
	default:
		return nil, errors.New("unknown mode")
	}
}
func query(o options) (any, error) {
	if o.padding < 0 || o.padding > 50000 {
		return nil, errors.New("padding must be between 0 and 50000")
	}
	if o.padding > 0 {
		o.sql += " /* " + strings.Repeat("fixture-שלום ", o.padding) + " */"
	}
	raw := os.Getenv(o.dsnEnv)
	if raw == "" {
		return nil, errors.New("fixture DSN is unset")
	}
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid fixture DSN")
	}
	if o.address != "" {
		endpoint.Host = o.address
	}
	values := endpoint.Query()
	if o.driver == "postgres" {
		values.Set("sslmode", "disable")
		if o.ca != "" {
			values.Set("sslmode", "verify-full")
			values.Set("sslrootcert", o.ca)
			values.Set("host", "localhost")
		}
	} else {
		values.Set("encrypt", "disable")
		if o.ca != "" {
			values.Set("encrypt", "true")
			values.Set("trustservercertificate", "false")
			values.Set("certificate", o.ca)
			values.Set("hostnameincertificate", "localhost")
		}
	}
	endpoint.RawQuery = values.Encode()
	db, err := sql.Open(o.driver, endpoint.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if o.mode == "active" {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = conn.Close() }()
		if err := conn.PingContext(ctx); err != nil {
			return nil, err
		}
		if err := os.WriteFile(o.capture, []byte("ready"), 0600); err != nil {
			return nil, err
		}
		statement := "SELECT pg_sleep(30)"
		if o.driver == "sqlserver" {
			statement = "WAITFOR DELAY '00:00:30'"
		}
		_, err = conn.ExecContext(ctx, statement)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			return nil, errors.New("active query completed without disconnect")
		}
		return map[string]any{"disconnected": true}, nil
	}
	var args []any
	if err := json.Unmarshal([]byte(o.args), &args); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, o.sql, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := [][]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"columns": columns, "rows": result}, nil
}
func capture(o options) (any, error) {
	file, err := os.Open(o.capture)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	result := []proxy.Statement{}
	for scanner.Scan() {
		var row proxy.Statement
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if strings.Contains(row.SQL, o.marker) {
			result = append(result, row)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return map[string]any{"records": result}, nil
}
func certificates(dir string) (any, error) {
	if dir == "" {
		return nil, errors.New("certificate directory required")
	}
	config, _, err := testcert.Configs()
	if err != nil {
		return nil, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(config.Certificates[0].PrivateKey)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	untrusted, _, err := testcert.Configs()
	if err != nil {
		return nil, err
	}
	for name, data := range map[string][]byte{"untrusted.pem": testcert.PEM(untrusted), "server.pem": testcert.PEM(config), "ca.pem": testcert.PEM(config), "server-key.pem": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return nil, err
		}
	}
	return map[string]any{"directory": dir}, nil
}
