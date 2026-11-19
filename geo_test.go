package main

import (
	"net/http/httptest"
	"testing"
)

func TestForwardedAddressRequiresTrustedPeer(t *testing.T) {
	geo, err := openGeo("", "127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	if geo.db != nil {
		t.Cleanup(func() { _ = geo.db.Close() })
	}
	for _, tc := range []struct {
		name, peer, header, want string
	}{
		{"trusted proxy", "127.0.0.1:8090", "2001:db8::1", "[2001:db8::1]:0"},
		{"direct client", "203.0.113.4:8090", "198.51.100.4", "203.0.113.4:8090"},
		{"invalid address", "127.0.0.1:8090", "198.51.100.1, 198.51.100.2", "127.0.0.1:8090"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/portal/context", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Real-IP", tc.header)
			geo.applyTrustedProxy(req)
			if req.RemoteAddr != tc.want {
				t.Fatalf("got peer %q; want %q", req.RemoteAddr, tc.want)
			}
		})
	}
}
