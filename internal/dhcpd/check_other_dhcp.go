// +build aix darwin dragonfly freebsd linux netbsd openbsd solaris

package dhcpd

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/AdguardTeam/AdGuardHome/internal/dhcpd/nclient4"
	"github.com/AdguardTeam/golibs/log"
	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
	"github.com/insomniacslk/dhcp/iana"
)

// CheckIfOtherDHCPServersPresentV4 sends a DHCP request to the specified network interface,
// and waits for a response for a period defined by defaultDiscoverTime
func CheckIfOtherDHCPServersPresentV4(ifaceName string) (bool, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return false, fmt.Errorf("couldn't find interface by name %s: %w", ifaceName, err)
	}

	ifaceIPNet, err := ifaceIPv4Addrs(iface)
	if err != nil {
		return false, fmt.Errorf("getting ipv4 addrs for iface %s: %w", ifaceName, err)
	}
	if len(ifaceIPNet) == 0 {
		return false, fmt.Errorf("interface %s has no ipv4 addresses", ifaceName)
	}

	// TODO(a.garipov): Find out what this is about.  Perhaps this
	// information is outdated or at least incomplete.
	if runtime.GOOS == "darwin" {
		return false, fmt.Errorf("can't find DHCP server: not supported on macOS")
	}

	srcIP := ifaceIPNet[0]
	src := net.JoinHostPort(srcIP.String(), "68")
	dst := "255.255.255.255:67"

	hostname, _ := os.Hostname()

	req, err := dhcpv4.NewDiscovery(iface.HardwareAddr)
	if err != nil {
		return false, fmt.Errorf("dhcpv4.NewDiscovery: %w", err)
	}
	req.Options.Update(dhcpv4.OptClientIdentifier(iface.HardwareAddr))
	req.Options.Update(dhcpv4.OptHostName(hostname))

	// resolve 0.0.0.0:68
	udpAddr, err := net.ResolveUDPAddr("udp4", src)
	if err != nil {
		return false, fmt.Errorf("couldn't resolve UDP address %s: %w", src, err)
	}

	if !udpAddr.IP.To4().Equal(srcIP) {
		return false, fmt.Errorf("resolved UDP address is not %s: %w", src, err)
	}

	// resolve 255.255.255.255:67
	dstAddr, err := net.ResolveUDPAddr("udp4", dst)
	if err != nil {
		return false, fmt.Errorf("couldn't resolve UDP address %s: %w", dst, err)
	}

	// bind to 0.0.0.0:68
	log.Tracef("Listening to udp4 %+v", udpAddr)
	c, err := nclient4.NewRawUDPConn(ifaceName, 68)
	if err != nil {
		return false, fmt.Errorf("couldn't listen on :68: %w", err)
	}
	if c != nil {
		defer c.Close()
	}

	// send to 255.255.255.255:67
	_, err = c.WriteTo(req.ToBytes(), dstAddr)
	if err != nil {
		return false, fmt.Errorf("couldn't send a packet to %s: %w", dst, err)
	}

	for {
		ok, next, err := tryConn(req, c, iface)
		if next {
			continue
		}
		if ok {
			return true, nil
		}
		if err != nil {
			log.Debug("%s", err)
		}
	}
}

// TODO(a.garipov): Refactor further.  Inspect error handling, remove the next
// parameter, address the TODO, etc.
func tryConn(req *dhcpv4.DHCPv4, c net.PacketConn, iface *net.Interface) (ok, next bool, err error) {
	// TODO: replicate dhclient's behavior of retrying several times with
	// progressively longer timeouts.
	log.Tracef("waiting %v for an answer", defaultDiscoverTime)

	b := make([]byte, 1500)
	_ = c.SetReadDeadline(time.Now().Add(defaultDiscoverTime))
	n, _, err := c.ReadFrom(b)
	if err != nil {
		if isTimeout(err) {
			log.Debug("dhcpv4: didn't receive dhcp response")

			return false, false, nil
		}

		return false, false, fmt.Errorf("receiving packet: %w", err)
	}

	log.Tracef("received packet, %d bytes", n)

	response, err := dhcpv4.FromBytes(b[:n])
	if err != nil {
		log.Debug("dhcpv4: encoding: %s", err)

		return false, true, err
	}

	log.Debug("dhcpv4: received message from server: %s", response.Summary())

	if !(response.OpCode == dhcpv4.OpcodeBootReply &&
		response.HWType == iana.HWTypeEthernet &&
		bytes.Equal(response.ClientHWAddr, iface.HardwareAddr) &&
		bytes.Equal(response.TransactionID[:], req.TransactionID[:]) &&
		response.Options.Has(dhcpv4.OptionDHCPMessageType)) {

		log.Debug("dhcpv4: received message from server doesn't match our request")

		return false, true, nil
	}

	log.Tracef("the packet is from an active dhcp server")

	return true, false, nil
}

// CheckIfOtherDHCPServersPresentV6 sends a DHCP request to the specified network interface,
// and waits for a response for a period defined by defaultDiscoverTime
func CheckIfOtherDHCPServersPresentV6(ifaceName string) (bool, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return false, fmt.Errorf("dhcpv6: net.InterfaceByName: %s: %w", ifaceName, err)
	}

	ifaceIPNet, err := ifaceIPv6Addrs(iface)
	if err != nil {
		return false, fmt.Errorf("getting ipv6 addrs for iface %s: %w", ifaceName, err)
	}
	if len(ifaceIPNet) == 0 {
		return false, fmt.Errorf("interface %s has no ipv6 addresses", ifaceName)
	}

	srcIP := ifaceIPNet[0]
	src := net.JoinHostPort(srcIP.String(), "546")
	dst := "[ff02::1:2]:547"

	req, err := dhcpv6.NewSolicit(iface.HardwareAddr)
	if err != nil {
		return false, fmt.Errorf("dhcpv6: dhcpv6.NewSolicit: %w", err)
	}

	udpAddr, err := net.ResolveUDPAddr("udp6", src)
	if err != nil {
		return false, fmt.Errorf("dhcpv6: Couldn't resolve UDP address %s: %w", src, err)
	}

	if !udpAddr.IP.To16().Equal(srcIP) {
		return false, fmt.Errorf("dhcpv6: Resolved UDP address is not %s: %w", src, err)
	}

	dstAddr, err := net.ResolveUDPAddr("udp6", dst)
	if err != nil {
		return false, fmt.Errorf("dhcpv6: Couldn't resolve UDP address %s: %w", dst, err)
	}

	log.Debug("DHCPv6: Listening to udp6 %+v", udpAddr)
	c, err := nclient6.NewIPv6UDPConn(ifaceName, dhcpv6.DefaultClientPort)
	if err != nil {
		return false, fmt.Errorf("dhcpv6: Couldn't listen on :546: %w", err)
	}
	if c != nil {
		defer c.Close()
	}

	_, err = c.WriteTo(req.ToBytes(), dstAddr)
	if err != nil {
		return false, fmt.Errorf("dhcpv6: Couldn't send a packet to %s: %w", dst, err)
	}

	for {
		log.Debug("DHCPv6: Waiting %v for an answer", defaultDiscoverTime)
		b := make([]byte, 4096)
		_ = c.SetReadDeadline(time.Now().Add(defaultDiscoverTime))
		n, _, err := c.ReadFrom(b)
		if isTimeout(err) {
			log.Debug("DHCPv6: didn't receive DHCP response")
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("couldn't receive packet: %w", err)
		}

		log.Debug("DHCPv6: Received packet (%v bytes)", n)

		resp, err := dhcpv6.FromBytes(b[:n])
		if err != nil {
			log.Debug("DHCPv6: dhcpv6.FromBytes: %s", err)
			continue
		}

		log.Debug("DHCPv6: received message from server: %s", resp.Summary())

		cid := req.Options.ClientID()
		msg, err := resp.GetInnerMessage()
		if err != nil {
			log.Debug("DHCPv6: resp.GetInnerMessage: %s", err)
			continue
		}
		rcid := msg.Options.ClientID()
		if resp.Type() == dhcpv6.MessageTypeAdvertise &&
			msg.TransactionID == req.TransactionID &&
			rcid != nil &&
			cid.Equal(*rcid) {
			log.Debug("DHCPv6: The packet is from an active DHCP server")
			return true, nil
		}

		log.Debug("DHCPv6: received message from server doesn't match our request")
	}
}
