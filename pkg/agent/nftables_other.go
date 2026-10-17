//go:build !linux

package agent

import (
	"errors"
	"net"
)

// nf_tables only exists on Linux; elsewhere the agent falls back to iptables
// (which is not there either, so partitions report an error).
type noNFTables struct{}

func newNFTablesBackend() filterBackend { return noNFTables{} }

func (noNFTables) Name() string    { return "nftables" }
func (noNFTables) Available() bool { return false }
func (noNFTables) Sync(_ []net.IP) error {
	return errors.New("nftables: not supported on this platform")
}
func (noNFTables) Teardown() error { return nil }
