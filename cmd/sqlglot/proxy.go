package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/sqlglot-go/proxy"
	"github.com/flanksource/sqlglot-go/proxy/postgres"
	"github.com/flanksource/sqlglot-go/proxy/tds"
	"github.com/spf13/cobra"
	"os"
	"os/signal"
)

type proxyFlags struct {
	options                                proxy.Options
	cert, key, upstreamTLS, ca, serverName string
}

func addProxyCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "proxy", Short: "Forward database traffic and stream observed queries"}
	entity.MarkLocalOnly(parent)
	for _, name := range []string{"postgres", "tds"} {
		flags := &proxyFlags{}
		cmd := &cobra.Command{Use: name, Short: "Run an observing " + name + " proxy", Args: cobra.NoArgs}
		cmd.RunE = func(cmd *cobra.Command, _ []string) error { return runProxy(cmd, name, flags) }
		f := cmd.Flags()
		f.StringVar(&flags.options.Upstream, "upstream", "", "Upstream host:port (required)")
		f.StringVar(&flags.options.Listen, "listen", "127.0.0.1:0", "Listener host:port")
		f.BoolVar(&flags.options.Params, "params", false, "Capture bind parameter values")
		f.StringVar(&flags.options.ClientTLSMode, "client-tls", "disable", "Client TLS: disable, allow, require")
		f.StringVar(&flags.cert, "tls-cert", "", "Client-facing PEM certificate")
		f.StringVar(&flags.key, "tls-key", "", "Client-facing PEM private key")
		f.StringVar(&flags.upstreamTLS, "upstream-tls", "disable", "Upstream TLS: disable, require (verified)")
		f.StringVar(&flags.ca, "upstream-ca", "", "PEM CA bundle for upstream TLS")
		f.StringVar(&flags.serverName, "upstream-server-name", "", "Upstream TLS certificate hostname")
		parent.AddCommand(cmd)
	}
	root.AddCommand(parent)
}
func (f *proxyFlags) configureTLS() error {
	if (f.cert == "") != (f.key == "") {
		return errors.New("both tls-cert and tls-key are required")
	}
	if f.cert != "" {
		cert, err := tls.LoadX509KeyPair(f.cert, f.key)
		if err != nil {
			return fmt.Errorf("client TLS certificate: %w", err)
		}
		f.options.ClientTLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	switch f.upstreamTLS {
	case "require":
		config := &tls.Config{ServerName: f.serverName, MinVersion: tls.VersionTLS12}
		if f.ca != "" {
			data, err := os.ReadFile(f.ca)
			if err != nil {
				return err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(data) {
				return errors.New("upstream-ca contains no certificates")
			}
			config.RootCAs = pool
		}
		f.options.UpstreamTLS = config
	case "disable":
		if f.ca != "" || f.serverName != "" {
			return errors.New("upstream TLS settings require --upstream-tls=require")
		}
	default:
		return errors.New("upstream-tls must be disable or require")
	}
	return nil
}
func runProxy(cmd *cobra.Command, protocol string, flags *proxyFlags) error {
	if err := flags.configureTLS(); err != nil {
		return err
	}
	jsonOutput, err := cmd.Flags().GetBool("json")
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	flags.options.OnStatement = func(row proxy.Statement) error {
		if jsonOutput {
			return encoder.Encode(row)
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s rows=%d duration=%s incomplete=%t error=%q\n", row.Connection, row.RPC, row.SQL, row.Rows, row.Duration, row.Incomplete, row.Error)
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	start := postgres.Start
	if protocol == "tds" {
		start = tds.Start
	}
	server, err := start(ctx, flags.options)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s proxy listening on %s -> %s\n", protocol, server.Addr(), flags.options.Upstream); err != nil {
		return err
	}
	return server.Wait()
}
