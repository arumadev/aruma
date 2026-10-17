package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the address of the client. X-Forwarded-For is believed only when the
// connection comes from a trusted proxy, and then only up to the first untrusted hop, reading
// from the right; anything to its left is client-supplied and may be forged.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer := remoteAddr(r)
	if !peer.IsValid() || !contains(trusted, peer) {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer
		}
		ip = ip.Unmap()
		if !contains(trusted, ip) {
			return ip
		}
		peer = ip
	}
	return peer
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}

func contains(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
