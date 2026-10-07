package provider

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"
)

// NeedsNetwork reports whether providers of this kind reach beyond the
// machine: Apple Shortcuts (Private Cloud Compute / ChatGPT), hosted API
// endpoints, and agent CLIs (claude). OnDevice (fm) and LocalServer
// (localhost servers) work with no network at all. An unknown kind is
// assumed local, so it is never skipped on a guess.
func (k Kind) NeedsNetwork() bool {
	switch k {
	case AppleCloud, API, AgentCLI:
		return true
	}
	return false
}

// routeProbes are documentation-only addresses (RFC 5737 / RFC 3849);
// nothing answers there, and no packet is ever sent.
var routeProbes = []struct{ network, addr string }{
	{"udp4", "192.0.2.1:9"},
	{"udp6", "[2001:db8::1]:9"},
}

// hasRoute reports whether the OS can pick a route for an IPv4 or IPv6
// destination. A UDP "connect" only consults the routing table (it sends
// nothing), so with no usable route it fails immediately with "network is
// unreachable" and when online it costs microseconds.
//
// Limits: this only proves a route exists. Wi-Fi without internet, a captive
// portal, or a VPN default route to nowhere all look online, so provider
// timeouts still apply there (a clear network-level error from an API
// provider then marks the rest of the chain offline; see IsNetworkError).
func hasRoute() bool {
	for _, p := range routeProbes {
		c, err := net.Dial(p.network, p.addr)
		if err == nil {
			c.Close()
			return true
		}
	}
	return false
}

// offlineCheck is the route test; a package var so tests can fake it.
var offlineCheck = hasRoute

var (
	offlineMu    sync.Mutex
	offlineKnown bool
	offlineVal   bool
)

// Offline reports whether this machine has no network route. The check
// runs lazily, at most once per process; Chain calls it only when it is
// about to call a network-dependent provider.
func Offline() bool {
	offlineMu.Lock()
	defer offlineMu.Unlock()
	if !offlineKnown {
		offlineVal = !offlineCheck()
		offlineKnown = true
	}
	return offlineVal
}

// resetOffline forgets the cached result (tests).
func resetOffline() {
	offlineMu.Lock()
	offlineKnown = false
	offlineMu.Unlock()
}

// OfflineSkippedError means Chain declined to call a network-dependent
// provider because the machine is offline (or an earlier provider failed
// with a clear network-level error); Generate was never invoked. Unlike a
// cooldown, --force does not override it: the call cannot succeed, and
// Shortcuts would post a failure notification for nothing. It satisfies
// errors.As(err, &*UnavailableError) via Unwrap.
type OfflineSkippedError struct {
	ID     string
	Reason string // e.g. "no network route"
}

func (e *OfflineSkippedError) Error() string {
	return fmt.Sprintf("%s skipped: offline (%s)", e.ID, e.Reason)
}

func (e *OfflineSkippedError) Unwrap() error {
	return &UnavailableError{ID: e.ID, Reason: "offline (" + e.Reason + ")"}
}

// networkErrorMarkers are substrings of Go's net/http error text that
// unambiguously mean the network, not the server, is the problem. A
// refused connection is deliberately absent: it usually means a server is
// simply not running and says nothing about connectivity.
var networkErrorMarkers = []string{
	"no such host",
	"network is unreachable",
	"no route to host",
	"tls handshake timeout",
	"temporary failure in name resolution",
}

// IsNetworkError reports whether err is a clear network-level failure (DNS
// lookup failure, no route, TLS handshake timeout). Conservative: anything
// ambiguous, including plain timeouts and refused connections, is false.
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, m := range networkErrorMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// offlineHint is appended to the chain's final error when it was cut short
// by being offline and nothing local was tried.
const offlineHint = "offline, and no local provider in the chain; add `fm` (or a local server such as lmstudio/ollama) to your providers"
