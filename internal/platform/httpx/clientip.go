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

// Host returns the host the client asked for, as it wrote it (port included, a trailing dot
// removed): X-Forwarded-Host when the connection comes from a trusted proxy and carries one,
// else the request's Host. A proxy in front of Aruma must keep Host or send
// X-Forwarded-Host, since some APIs are named by host (ADR-018).
func Host(r *http.Request, trusted []netip.Prefix) string {
	host := r.Host
	if peer := remoteAddr(r); peer.IsValid() && contains(trusted, peer) {
		if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
			// A chain of proxies appends; the first value is what the client sent.
			first, _, _ := strings.Cut(fwd, ",")
			host = strings.TrimSpace(first)
		}
	}
	if h, port, err := net.SplitHostPort(host); err == nil {
		return net.JoinHostPort(strings.TrimSuffix(h, "."), port)
	}
	return strings.TrimSuffix(host, ".")
}

// PeerIsLoopback reports whether the connection itself comes from this machine. Forwarded
// headers play no part: a proxy on this machine makes every client look local.
func PeerIsLoopback(r *http.Request) bool {
	peer := remoteAddr(r)
	return peer.IsValid() && peer.IsLoopback()
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
