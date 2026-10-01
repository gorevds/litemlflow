package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// AllowPrivateEnv is the operator opt-out for the outbound SSRF guard. When
// set to "1", server-initiated HTTP (webhooks, federation peers) may target
// loopback / private / link-local addresses.
const AllowPrivateEnv = "LITEMLFLOW_WEBHOOK_ALLOW_PRIVATE"

// ErrBlockedAddress is returned (wrapped) when an outbound connection would
// reach a blocked (private, loopback, link-local, metadata, …) address.
var ErrBlockedAddress = errors.New("outbound connection to a blocked address")

// AllowPrivateTargets reports whether the operator disabled the SSRF guard.
// It is read on every call so tests (and operators restarting with a new
// env) see the current value.
func AllowPrivateTargets() bool {
	return strings.TrimSpace(os.Getenv(AllowPrivateEnv)) == "1"
}

// extra IPv4 ranges not covered by net.IP's Is* helpers that are either
// non-routable on the public internet or commonly used for internal
// infrastructure (CGNAT is where e.g. Alibaba Cloud's metadata service
// 100.100.100.200 lives; 0.0.0.0/8 connects to the local host on Linux).
var blockedV4Nets = mustCIDRs(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"198.18.0.0/15",
	"240.0.0.0/4",
)

// IPv6 prefixes that embed an IPv4 address the kernel/NAT may route to.
var (
	nat64Net = mustCIDRs("64:ff9b::/96")[0]
	sixTo4   = mustCIDRs("2002::/16")[0]
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

// IsBlockedIP returns true for addresses a server-initiated request must not
// reach: loopback, link-local (incl. 169.254.169.254 cloud metadata),
// RFC1918 / unique-local, unspecified, multicast, CGNAT, reserved ranges, and
// IPv6 forms that embed such an IPv4 address (IPv4-mapped, NAT64, 6to4).
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		if v4.Equal(net.IPv4bcast) {
			return true
		}
		for _, n := range blockedV4Nets {
			if n.Contains(v4) {
				return true
			}
		}
		return false
	}
	// IPv6 forms embedding an IPv4 address.
	if nat64Net.Contains(ip) {
		return IsBlockedIP(net.IP(ip[12:16]))
	}
	if sixTo4.Contains(ip) {
		return IsBlockedIP(net.IP(ip[2:6]))
	}
	return false
}

// guardedDialContext resolves the target host itself and only dials
// addresses that pass IsBlockedIP. Checking at dial time (rather than only
// when a URL is saved) closes DNS-rebinding and redirect-based bypasses: the
// address actually connected to is the one that was checked.
//
// Dials to a configured HTTP(S) proxy are exempt — with a proxy the
// destination is reached by the proxy, which the operator controls.
func guardedDialContext(d *net.Dialer, proxyAddrs map[string]bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if AllowPrivateTargets() || proxyAddrs[addr] {
			return d.DialContext(ctx, network, addr)
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ipa := range ips {
			if IsBlockedIP(ipa.IP) {
				lastErr = fmt.Errorf("%w: %s resolves to %s (set %s=1 to allow)",
					ErrBlockedAddress, host, ipa.IP, AllowPrivateEnv)
				continue
			}
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no addresses for %s", host)
		}
		return nil, lastErr
	}
}

// proxyAddrsFromEnv returns the host:port of any HTTP(S) proxy configured via
// the standard environment variables (the same ones http.ProxyFromEnvironment
// honours).
func proxyAddrsFromEnv() map[string]bool {
	out := map[string]bool{}
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			continue
		}
		if !strings.Contains(v, "://") {
			v = "http://" + v
		}
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			continue
		}
		port := u.Port()
		if port == "" {
			switch u.Scheme {
			case "https":
				port = "443"
			case "socks5", "socks5h":
				port = "1080"
			default:
				port = "80"
			}
		}
		out[net.JoinHostPort(u.Hostname(), port)] = true
	}
	return out
}

var (
	guardedTransportOnce sync.Once
	guardedTransport     *http.Transport
)

// sharedGuardedTransport returns a process-wide transport whose dialer
// enforces IsBlockedIP. Shared so per-request clients (federation fan-out)
// reuse one connection pool instead of leaking idle connections.
func sharedGuardedTransport() *http.Transport {
	guardedTransportOnce.Do(func() {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		tr.DialContext = guardedDialContext(d, proxyAddrsFromEnv())
		guardedTransport = tr
	})
	return guardedTransport
}

// NewGuardedClient returns an HTTP client for server-initiated outbound
// requests to user-supplied URLs (webhooks, federation peers):
//
//   - every dialed address is checked with IsBlockedIP (unless
//     LITEMLFLOW_WEBHOOK_ALLOW_PRIVATE=1), defeating DNS rebinding;
//   - redirects are NOT followed — a 3xx is returned to the caller as-is, so
//     a public endpoint cannot bounce the request (and its signature
//     headers) to another host.
func NewGuardedClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: sharedGuardedTransport(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ---- workspace propagation --------------------------------------------------

type workspaceCtxKey struct{}

// WithWorkspace annotates ctx with the workspace the triggering request ran
// in. Dispatcher.Notify uses it to select only that workspace's webhooks.
func WithWorkspace(ctx context.Context, workspaceID string) context.Context {
	return context.WithValue(ctx, workspaceCtxKey{}, workspaceID)
}

// WorkspaceFromContext returns the workspace set by WithWorkspace, or "".
func WorkspaceFromContext(ctx context.Context) string {
	if ws, ok := ctx.Value(workspaceCtxKey{}).(string); ok {
		return ws
	}
	return ""
}
