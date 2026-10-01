package telemetrycfg

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ProbeTimeout bounds the collector reachability probe.
const ProbeTimeout = 750 * time.Millisecond

// hostPort extracts the dial address from an OTLP/HTTP endpoint, which is
// either "host:port" or a URL such as "http://127.0.0.1:4318". A URL with no
// port gets the scheme default; the OTLP/HTTP default port 4318 is used for a
// bare host.
func hostPort(endpoint string) (string, error) {
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid OTLP endpoint %q", endpoint)
	}
	port := u.Port()
	if port == "" {
		switch {
		case u.Scheme == "https":
			port = "443"
		case u.Scheme == "http" && strings.Contains(endpoint, "://"):
			port = "80"
		default:
			port = "4318"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// Probe reports whether something accepts TCP connections at the endpoint. It
// opens one connection and closes it without sending anything, so a collector
// sees no spurious OTLP request. Callers MUST only probe when telemetry is
// enabled (the disabled path opens no connection at all).
func Probe(ctx context.Context, endpoint string, timeout time.Duration) error {
	addr, err := hostPort(endpoint)
	if err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = ProbeTimeout
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// UnreachableMessage is the single stderr line printed under -v when
// telemetry is enabled but the collector does not answer.
func UnreachableMessage(endpoint string) string {
	return fmt.Sprintf("telemetry disabled: collector unreachable (%s)", endpoint)
}
