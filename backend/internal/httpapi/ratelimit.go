package httpapi

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRoomCreateLimitPerMin = 20
	defaultRoomJoinLimitPerMin   = 80
	defaultWSConnectLimitPerMin  = 120
	defaultWSMessageLimitPerMin  = 240
)

type rateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]rateBucket
	attempts uint64
	now      func() time.Time
}

type rateBucket struct {
	windowStart time.Time
	count       int
	lastSeen    time.Time
}

type proxyHeaderConfig struct {
	trustAll bool
	nets     []*net.IPNet
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: map[string]rateBucket{}, now: time.Now}
}

func (l *rateLimiter) allow(key string, limit int, window time.Duration) bool {
	if limit <= 0 {
		return true
	}
	if window <= 0 {
		window = time.Minute
	}
	now := l.now().UTC()

	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= window {
		b.windowStart = now
		b.count = 0
	}
	b.count++
	b.lastSeen = now
	l.buckets[key] = b

	l.attempts++
	if l.attempts%256 == 0 {
		l.pruneLocked(now, 3*window)
	}
	return b.count <= limit
}

func (l *rateLimiter) pruneLocked(now time.Time, maxIdle time.Duration) {
	for key, bucket := range l.buckets {
		if bucket.lastSeen.IsZero() || now.Sub(bucket.lastSeen) > maxIdle {
			delete(l.buckets, key)
		}
	}
}

func newProxyHeaderConfig() proxyHeaderConfig {
	cfg := proxyHeaderConfig{trustAll: truthyEnv(os.Getenv("PUNCHLINE_TRUST_PROXY_HEADERS"))}
	for _, part := range strings.Split(os.Getenv("PUNCHLINE_TRUSTED_PROXY_CIDRS"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, cidr := range expandProxyCIDRToken(part) {
			if _, network, err := net.ParseCIDR(cidr); err == nil {
				cfg.nets = append(cfg.nets, network)
			}
		}
	}
	return cfg
}

func expandProxyCIDRToken(token string) []string {
	switch strings.ToLower(token) {
	case "loopback":
		return []string{"127.0.0.0/8", "::1/128"}
	case "private":
		return []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}
	default:
		return []string{token}
	}
}

func (cfg proxyHeaderConfig) clientIP(r *http.Request) string {
	remote := remoteIP(r)
	if cfg.trusts(remote) {
		if headerIP := forwardedIP(r); headerIP != "" {
			return headerIP
		}
	}
	if remote != "" {
		return remote
	}
	return r.RemoteAddr
}

func (cfg proxyHeaderConfig) trusts(remote string) bool {
	if remote == "" {
		return false
	}
	ip := net.ParseIP(strings.Trim(remote, "[]"))
	if ip == nil {
		return false
	}
	if cfg.trustAll {
		return true
	}
	for _, network := range cfg.nets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func forwardedIP(r *http.Request) string {
	for _, header := range []string{"Fly-Client-IP", "X-Real-IP", "X-Forwarded-For"} {
		raw := strings.TrimSpace(r.Header.Get(header))
		if raw == "" {
			continue
		}
		if header == "X-Forwarded-For" {
			raw = strings.TrimSpace(strings.Split(raw, ",")[0])
		}
		if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
			return ip.String()
		}
	}
	return ""
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			return ip.String()
		}
		return host
	}
	if ip := net.ParseIP(strings.Trim(r.RemoteAddr, "[]")); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

func getenvLimit(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func truthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
