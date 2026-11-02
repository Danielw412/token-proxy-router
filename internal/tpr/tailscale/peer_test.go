package tailscale

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

const procTable = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:207D 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 11111 1 0 100 0 0 10 0
   1: 0100007F:C350 0100007F:207D 01 00000000:00000000 00:00000000 00000000     0        0 22222 1 0 20 4 30 10 -1
   2: 0100007F:C351 0100007F:207D 01 00000000:00000000 00:00000000 00000000  1001        0 33333 1 0 20 4 30 10 -1
   3: 0100007F:207D 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 44444 1 0 20 4 30 10 -1
`

const procTable6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:C352 00000000000000000000000001000000:207D 01 00000000:00000000 00:00000000 00000000     0        0 55555 1 0 20 4 30 10 -1
   1: 0000000000000000FFFF00000100007F:C353 0000000000000000FFFF00000100007F:207D 01 00000000:00000000 00:00000000 00000000  1001        0 66666 1 0 20 4 30 10 -1
`

func addrPort(t *testing.T, s string) netip.AddrPort {
	t.Helper()
	parsed, err := netip.ParseAddrPort(s)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestPeerUIDFromTable(t *testing.T) {
	server := addrPort(t, "127.0.0.1:8317")
	for _, tc := range []struct {
		name  string
		table string
		peer  netip.AddrPort
		srv   netip.AddrPort
		uid   uint32
		found bool
	}{
		{"owned by root", procTable, addrPort(t, "127.0.0.1:50000"), server, 0, true},
		{"owned by another user", procTable, addrPort(t, "127.0.0.1:50001"), server, 1001, true},
		{"the server's own end is not the peer", procTable, addrPort(t, "127.0.0.1:8317"), addrPort(t, "127.0.0.1:50000"), 1000, true},
		{"unknown connection", procTable, addrPort(t, "127.0.0.1:50002"), server, 0, false},
		{"other server port", procTable, addrPort(t, "127.0.0.1:50000"), addrPort(t, "127.0.0.1:9999"), 0, false},
		{"IPv6 loopback", procTable6, addrPort(t, "[::1]:50002"), addrPort(t, "[::1]:8317"), 0, true},
		{"IPv4-mapped", procTable6, addrPort(t, "127.0.0.1:50003"), server, 1001, true},
		{"empty table", "", addrPort(t, "127.0.0.1:50000"), server, 0, false},
		{"garbage", "this is not\na table at all, not even close to one", addrPort(t, "127.0.0.1:50000"), server, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid, found := peerUIDFromTable(tc.table, tc.peer, tc.srv)
			if found != tc.found || uid != tc.uid {
				t.Fatalf("got uid=%d found=%v, want uid=%d found=%v", uid, found, tc.uid, tc.found)
			}
		})
	}
}

func TestDecodeProcAddrRejectsBadInput(t *testing.T) {
	for _, field := range []string{"", "0100007F", "ZZ00007F:207D", "0100007F:ZZZZ", "0100:207D", "0100007F:12345"} {
		if _, ok := decodeProcAddr(field); ok {
			t.Errorf("%q should be rejected", field)
		}
	}
}

func TestRequestAddrsNeedsTheServerAddress(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	if _, _, ok := requestAddrs(r); ok {
		t.Fatal("a request with no server address must not be trusted")
	}
}

// A real connection on this machine must resolve to this user on Linux, and to
// "not trusted" everywhere else.
func TestDefaultPeerTrustedOnARealConnection(t *testing.T) {
	var got bool
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = defaultPeerTrusted(r)
	}))
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got != identitySupported {
		t.Fatalf("trusted = %v, want %v on this OS", got, identitySupported)
	}
}

func TestCheckRefusesAnUntrustedLocalPeer(t *testing.T) {
	checker := newChecker(okWhois)
	checker.PeerTrusted = func(*http.Request) bool { return false }
	if login, ok := checker.Check(serveRequest(), "127.0.0.1"); ok {
		t.Fatalf("a forged local request was accepted as %q", login)
	}
}

func TestCheckDefaultsToTheRealPeerCheck(t *testing.T) {
	checker := newChecker(okWhois)
	checker.PeerTrusted = nil
	// This request has no server address, so the real check must refuse it.
	if login, ok := checker.Check(serveRequest(), "127.0.0.1"); ok {
		t.Fatalf("accepted as %q", login)
	}
}
