package agent

import (
	"fmt"
	"net"
	"time"

	"vaultkeeper/internal/netutil"
)

// MagicPacket builds the Wake-on-LAN payload: 6 x 0xFF followed by the MAC 16 times.
func MagicPacket(mac net.HardwareAddr) []byte {
	p := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		p = append(p, 0xff)
	}
	for i := 0; i < 16; i++ {
		p = append(p, mac...)
	}
	return p
}

// wolTargets lists where to send the packet: the configured broadcast address,
// or every IPv4 broadcast address of this machine's interfaces plus the limited broadcast.
func wolTargets(broadcast string) ([]*net.UDPAddr, error) {
	if broadcast != "" {
		host, port := broadcast, "9"
		if h, p, err := net.SplitHostPort(broadcast); err == nil {
			host, port = h, p
		}
		a, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, port))
		if err != nil {
			return nil, fmt.Errorf("broadcast address: %w", err)
		}
		return []*net.UDPAddr{a}, nil
	}
	seen := map[string]bool{}
	var out []*net.UDPAddr
	add := func(ip net.IP) {
		if ip4 := ip.To4(); ip4 != nil && !seen[ip4.String()] {
			seen[ip4.String()] = true
			out = append(out, &net.UDPAddr{IP: ip4, Port: 9})
		}
	}
	add(net.IPv4bcast)
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && len(n.Mask) == 4 || ok && len(n.Mask) == 16 {
				ip, m := n.IP.To4(), n.Mask
				if len(m) == 16 {
					m = m[12:]
				}
				if ip == nil {
					continue
				}
				b := make(net.IP, 4)
				for i := range b {
					b[i] = ip[i] | ^m[i]
				}
				add(b)
			}
		}
	}
	return out, nil
}

// SendWOL sends the magic packet (three times, for reliability) and returns how many destinations were used.
func SendWOL(macStr, broadcast string) (int, error) {
	mac, err := netutil.ParseMAC(macStr)
	if err != nil {
		return 0, err
	}
	targets, err := wolTargets(broadcast)
	if err != nil {
		return 0, err
	}
	pkt := MagicPacket(mac)
	sent := 0
	var last error
	for _, t := range targets {
		c, err := net.DialUDP("udp4", nil, t)
		if err != nil {
			last = err
			continue
		}
		ok := false
		for i := 0; i < 3; i++ {
			if _, err := c.Write(pkt); err == nil {
				ok = true
			} else {
				last = err
			}
			time.Sleep(50 * time.Millisecond)
		}
		c.Close()
		if ok {
			sent++
		}
	}
	if sent == 0 {
		if last == nil {
			last = fmt.Errorf("no broadcast destination available")
		}
		return 0, last
	}
	return sent, nil
}
