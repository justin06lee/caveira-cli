//go:build devserver

package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Only a development build (package devserver) can send model requests to
// a model server on this machine or the local network, for tests and work
// on caveira itself; hosted endpoints and API keys of one's own are not
// taken even there. A shipped build has local_shipped.go instead, which
// names no server at all.

// CheckLocal says why baseURL is not a model server caveira can send to,
// or nil when it is: http or https, to localhost, a loopback or private
// network address (127/8, ::1, 10/8, 172.16/12, 192.168/16, fc00::/7), or
// a .local name. Names are not looked up, so a public name that happens
// to resolve to a private address is still refused.
func CheckLocal(baseURL string) error {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("%q is not a model server's address: it needs to look like http://localhost:11434/v1", baseURL)
	}
	if !localHost(u.Hostname()) {
		return fmt.Errorf("%s is not on this machine or your network: caveira only runs local models (localhost, 10.x, 172.16-31.x, 192.168.x or a .local name), or hosted ones through your caveira account", u.Hostname())
	}
	return nil
}

// IsLocal says baseURL names a local model server.
func IsLocal(baseURL string) bool { return baseURL != "" && CheckLocal(baseURL) == nil }

func localHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	if name, ok := strings.CutSuffix(host, ".local"); ok && name != "" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	// 0.0.0.0 is what servers print when they listen on every address;
	// connecting to it reaches this machine.
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
}
