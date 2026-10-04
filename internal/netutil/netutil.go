// Package netutil holds small network helpers shared by the manager (validation) and agent (use).
package netutil

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// ParseMAC accepts aa:bb:cc:dd:ee:ff, aa-bb-cc-dd-ee-ff, aabb.ccdd.eeff or aabbccddeeff.
func ParseMAC(s string) (net.HardwareAddr, error) {
	clean := strings.ToLower(strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.TrimSpace(s)))
	if len(clean) != 12 {
		return nil, fmt.Errorf("%q is not a valid MAC address", s)
	}
	hw := make(net.HardwareAddr, 6)
	for i := range hw {
		var b byte
		if _, err := fmt.Sscanf(clean[i*2:i*2+2], "%02x", &b); err != nil {
			return nil, fmt.Errorf("%q is not a valid MAC address", s)
		}
		hw[i] = b
	}
	return hw, nil
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// ValidHost guards against option injection when a host is passed to ping.
func ValidHost(h string) bool { return len(h) <= 253 && hostRe.MatchString(h) }

// ValidBroadcast accepts "ip" or "ip:port".
func ValidBroadcast(b string) bool {
	if b == "" {
		return true
	}
	host := b
	if h, p, err := net.SplitHostPort(b); err == nil {
		host = h
		if n := 0; p == "" || func() bool { _, e := fmt.Sscanf(p, "%d", &n); return e != nil || n < 1 || n > 65535 }() {
			return false
		}
	}
	return net.ParseIP(host) != nil
}
