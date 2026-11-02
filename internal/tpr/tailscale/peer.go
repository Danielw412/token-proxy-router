package tailscale

import (
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// A loopback address does not prove who connected: any local user can reach the
// listener and write Tailscale's headers. So identity sign-in also checks which
// OS user owns the other end of the connection, and fails closed when it can't.

// requestAddrs returns the client and server ends of a request's connection.
func requestAddrs(r *http.Request) (peer, server netip.AddrPort, ok bool) {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return peer, server, false
	}
	local, isAddr := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !isAddr || local == nil {
		return peer, server, false
	}
	server, err = netip.ParseAddrPort(local.String())
	if err != nil {
		return peer, server, false
	}
	return netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port()), netip.AddrPortFrom(server.Addr().Unmap(), server.Port()), true
}

// peerUIDFromTable finds the owner of the socket connecting peer to server in the
// text of /proc/net/tcp or /proc/net/tcp6.
func peerUIDFromTable(table string, peer, server netip.AddrPort) (uint32, bool) {
	const (
		fieldLocal  = 1
		fieldRemote = 2
		fieldUID    = 7
	)
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) <= fieldUID {
			continue
		}
		local, okLocal := decodeProcAddr(fields[fieldLocal])
		remote, okRemote := decodeProcAddr(fields[fieldRemote])
		if !okLocal || !okRemote || local != peer || remote != server {
			continue
		}
		uid, err := strconv.ParseUint(fields[fieldUID], 10, 32)
		if err != nil {
			return 0, false
		}
		return uint32(uid), true
	}
	return 0, false
}

// decodeProcAddr parses an address like 0100007F:2075. Each 32-bit word is in the
// kernel's byte order, which is little-endian on every platform TPR ships for. On
// any other the addresses simply never match, which fails closed.
func decodeProcAddr(field string) (netip.AddrPort, bool) {
	host, portHex, found := strings.Cut(field, ":")
	if !found {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	for i := 0; i+4 <= len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), true
}
