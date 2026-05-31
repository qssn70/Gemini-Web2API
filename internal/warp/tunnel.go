package warp

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Tunnel holds the running WireGuard tunnel and exposes the local
// SOCKS5 address that tls-client can use as a proxy.
type Tunnel struct {
	sAddr string
	ln    net.Listener
	dev   *device.Device
}

var (
	runningTunnel *Tunnel
	tunnelMu      sync.Mutex
)

// Start launches the WARP tunnel, performs device registration if
// needed, and returns a *Tunnel whose SOCKS5Addr() can be used as
// PROXY_URL. It is safe to call multiple times — a second call returns
// the existing tunnel.
func Start(cfg *Config, baseDir string) (*Tunnel, error) {
	tunnelMu.Lock()
	defer tunnelMu.Unlock()

	if runningTunnel != nil {
		return runningTunnel, nil
	}

	var wCfg *warpConfig
	var err error

	if cfg.PrivateKey == "" {
		// First run — register a brand new device.
		log.Println("[WARP] No device key found, registering new WARP identity...")
		wCfg, err = cfg.Register()
		if err != nil {
			return nil, fmt.Errorf("warp registration failed: %w", err)
		}
		cfg.SaveDevice(baseDir)
		log.Printf("[WARP] Registered device %s", cfg.DeviceID[:minLen(8, len(cfg.DeviceID))])
	} else {
		// Returning device — fetch the latest WireGuard config.
		log.Printf("[WARP] Existing device %s, fetching WireGuard config...",
			cfg.DeviceID[:minLen(8, len(cfg.DeviceID))])
		wCfg, err = cfg.GetDeviceConfig()
		if err != nil {
			// Token might be stale; try re-registering.
			log.Printf("[WARP] GetDeviceConfig failed (%v), re-registering...", err)
			wCfg, err = cfg.Register()
			if err != nil {
				return nil, fmt.Errorf("warp re-registration failed: %w", err)
			}
			cfg.SaveDevice(baseDir)
		}
	}

	if wCfg == nil || len(wCfg.Peers) == 0 {
		return nil, fmt.Errorf("warp config has no peers")
	}

	privBytes, err := base64.StdEncoding.DecodeString(cfg.PrivateKey)
	if err != nil || len(privBytes) != 32 {
		return nil, fmt.Errorf("invalid warp private key")
	}

	// Pick the first peer.
	peer := wCfg.Peers[0]

	// The peer public key from the API is base64-encoded.
	peerPubBytes, err := base64.StdEncoding.DecodeString(peer.PublicKey)
	if err != nil || len(peerPubBytes) != 32 {
		return nil, fmt.Errorf("invalid peer public key: %w", err)
	}

	// Resolve the endpoint address. The API returns either a bare IP
	// (e.g. "162.159.192.1") or a hostname:port (e.g.
	// "engage.cloudflareclient.com:2408"). The WireGuard config needs
	// a host:port string; if no port is present, default to 2408.
	endpoint := resolveEndpoint(peer.Endpoint)

	// Determine our tunnel IP. The API returns a /32 (v4) and /128 (v6).
	tunAddr := wCfg.Interface.Addresses.V4
	if tunAddr == "" {
		tunAddr = "172.16.0.2"
	}
	dnsAddr := "172.16.0.1"

	tun, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(tunAddr)},
		[]netip.Addr{netip.MustParseAddr(dnsAddr)},
		1420,
	)
	if err != nil {
		return nil, fmt.Errorf("create warp tun: %w", err)
	}

	var localKey [32]byte
	copy(localKey[:], privBytes)
	var peerKey [32]byte
	copy(peerKey[:], peerPubBytes)

	wgCfg := fmt.Sprintf("private_key=%x\npublic_key=%x\nendpoint=%s\npersistent_keepalive_interval=25\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\n",
		localKey, peerKey, endpoint)

	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, "[WARP] "))
	if err := dev.IpcSet(wgCfg); err != nil {
		return nil, fmt.Errorf("configure warp device: %w", err)
	}
	if err := dev.Up(); err != nil {
		return nil, fmt.Errorf("bring up warp device: %w", err)
	}

	// Listen on the HOST's loopback — not inside the tunnel's netstack.
	// tls-client connects to 127.0.0.1, which lives in the host network
	// namespace. The SOCKS5 handler then dials targets through tnet.
	port, _ := strconv.Atoi(SOCKS5Port)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			dev.Close()
			return nil, fmt.Errorf("listen socks5: %w", err)
		}
	}

	sAddr := "socks5://" + ln.Addr().String()

	t := &Tunnel{sAddr: sAddr, ln: ln, dev: dev}
	go t.serveSocks5(tnet)

	runningTunnel = t
	log.Printf("[WARP] Tunnel up, SOCKS5 proxy at %s (tunnel IP: %s, peer: %s)", sAddr, tunAddr, endpoint)
	return t, nil
}

// SOCKS5Addr returns the SOCKS5 proxy URL for use with tls-client.
func (t *Tunnel) SOCKS5Addr() string {
	return t.sAddr
}

// Close gracefully shuts down the WARP tunnel.
func Close() {
	tunnelMu.Lock()
	defer tunnelMu.Unlock()
	if runningTunnel != nil {
		runningTunnel.ln.Close()
		runningTunnel.dev.Close()
		runningTunnel = nil
		log.Println("[WARP] Tunnel closed")
	}
}

// resolveEndpoint normalises the endpoint from the API response into a
// "IP:port" string that WireGuard understands. The API sometimes returns
// just an IP without a port, or a hostname:port combo (which we must
// resolve to an IP because WireGuard's userspace stack can't do DNS).
func resolveEndpoint(ep warpEndpointInfo) string {
	host := ep.Host
	if host == "" {
		host = ep.V4
	}
	if host == "" {
		host = "engage.cloudflareclient.com:2408" // well-known fallback
	}

	// Split host:port — if no port, default to 2408.
	addr, port := splitHostPort(host, "2408")

	// If addr is already an IP, return as-is.
	if net.ParseIP(addr) != nil {
		return net.JoinHostPort(addr, port)
	}

	// It's a hostname — resolve it.
	ips, err := net.LookupIP(addr)
	if err != nil || len(ips) == 0 {
		log.Printf("[WARP] DNS lookup for %s failed (%v), trying well-known IP fallback", addr, err)
		// Fall back to one of Cloudflare's well-known WARP IPs.
		return "162.159.192.1:" + port
	}
	// Prefer IPv4 for WireGuard endpoints (more reliable on typical
	// server setups).
	for _, ip := range ips {
		if ip.To4() != nil {
			return net.JoinHostPort(ip.String(), port)
		}
	}
	return net.JoinHostPort(ips[0].String(), port)
}

// splitHostPort splits "host:port" or "[::1]:port" into (host, port).
// If no port is present, defaultPort is returned.
func splitHostPort(hostport, defaultPort string) (string, string) {
	// Handle IPv6 bracket notation.
	if strings.HasPrefix(hostport, "[") {
		if idx := strings.Index(hostport, "]:"); idx >= 0 {
			return hostport[1:idx], hostport[idx+2:]
		}
		return strings.Trim(hostport, "[]"), defaultPort
	}
	// host:port or just host.
	if idx := strings.LastIndex(hostport, ":"); idx >= 0 {
		return hostport[:idx], hostport[idx+1:]
	}
	return hostport, defaultPort
}

// --- minimal SOCKS5 implementation (CONNECT only, no auth) ---

func (t *Tunnel) serveSocks5(tnet *netstack.Net) {
	for {
		conn, err := t.ln.Accept()
		if err != nil {
			if runningTunnel == nil { // shutting down
				return
			}
			log.Printf("[WARP/SOCKS5] accept error: %v", err)
			continue
		}
		go handleSocks5Conn(conn, tnet)
	}
}

func handleSocks5Conn(client net.Conn, tnet *netstack.Net) {
	defer client.Close()

	// Negotiation
	buf := make([]byte, 256)
	if _, err := client.Read(buf[:2]); err != nil {
		return
	}
	nMethods := int(buf[1])
	if _, err := client.Read(buf[:nMethods]); err != nil {
		return
	}
	client.Write([]byte{0x05, 0x00}) // no auth

	// Request
	if _, err := client.Read(buf[:4]); err != nil {
		return
	}
	if buf[1] != 0x01 { // only CONNECT
		client.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := client.Read(buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // Domain
		if _, err := client.Read(buf[:1]); err != nil {
			return
		}
		dLen := int(buf[0])
		if _, err := client.Read(buf[:dLen]); err != nil {
			return
		}
		host = string(buf[:dLen])
	case 0x04: // IPv6
		if _, err := client.Read(buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		client.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	if _, err := client.Read(buf[:2]); err != nil {
		return
	}
	port := int(buf[0])<<8 | int(buf[1])

	target := net.JoinHostPort(host, strconv.Itoa(port))
	remote, err := tnet.DialContext(context.Background(), "tcp", target)
	if err != nil {
		client.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remote.Close()

	// Success
	client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyData(remote, client) }()
	go func() { defer wg.Done(); copyData(client, remote) }()
	wg.Wait()
}

func copyData(dst, src net.Conn) {
	defer src.Close()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
