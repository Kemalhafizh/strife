package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"

	"github.com/kemalhafizh/strife/internal/secret"
)

// ConnOptions configures authentication, TLS and isolation for the clients
// that support them (PostgreSQL, MySQL/TiDB, Redis/Valkey).
type ConnOptions struct {
	User     string
	Password string // resolved; never logged
	Database string
	// TLS is "disable" (default), "require" (encrypt, accept any server
	// certificate) or "verify-full" (verify the chain against CA, or the
	// system roots, and the host name).
	TLS string
	CA  string // PEM file of trusted CAs for verify-full
	// Isolation is the SQL isolation level transactions run at: "read
	// committed", "repeatable read" or "serializable".
	Isolation string
	// InitSQL runs once on every new connection, e.g. to select TiDB's
	// optimistic transaction mode.
	InitSQL string
}

// sqlOptions resolves the database settings of c.
func connOptions(c Config, defUser, defDB string) (ConnOptions, error) {
	level := c.Isolation
	if c.TxnIsolation != "" {
		level = c.TxnIsolation
	}
	o := ConnOptions{User: c.User, Database: c.Database, TLS: c.TLS, CA: c.TLSCA, Isolation: sqlIsolation(level), InitSQL: c.InitSQL}
	if o.User == "" {
		o.User = defUser
	}
	if o.Database == "" {
		o.Database = defDB
	}
	pw, err := secret.Resolve(c.Password)
	if err != nil {
		return o, fmt.Errorf("db.password: %w", err)
	}
	o.Password = pw
	switch o.TLS {
	case "", "disable", "require", "verify-full":
	default:
		return o, fmt.Errorf("db.tls %q: want disable, require or verify-full", o.TLS)
	}
	return o, nil
}

// sqlIsolation maps Strife's isolation names to SQL's.
func sqlIsolation(level string) string {
	switch level {
	case "read-uncommitted":
		return "READ UNCOMMITTED"
	case "read-committed":
		return "READ COMMITTED"
	case "snapshot-isolation", "repeatable-read":
		return "REPEATABLE READ"
	case "", "serializable", "strict-serializable":
		return "SERIALIZABLE"
	}
	return "SERIALIZABLE"
}

// tlsConfig builds the client TLS configuration for addr, or nil when TLS is
// disabled.
func (o ConnOptions) tlsConfig(addr string) (*tls.Config, error) {
	switch o.TLS {
	case "", "disable":
		return nil, nil
	case "require":
		// Encryption without authentication, like libpq's sslmode=require.
		return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, nil //nolint:gosec // requested explicitly
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if o.CA != "" {
		pem, err := os.ReadFile(o.CA)
		if err != nil {
			return nil, fmt.Errorf("db.tls_ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("db.tls_ca %s: no PEM certificates", o.CA)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}
