// Package bridgedns is a Caddy app that answers DNS queries for the
// wildcard-bridge variant's personal-tailnet Split DNS setup (SPEC §7.3,
// §10.1).
//
// For each configured community, the app binds UDP/53 on the
// community's tsnet listener node (the same node Caddy uses for HTTPS,
// shared via caddy-tailscale's `tailscale/udp/<node>:port` registered
// network) and answers `*.<community.domain>` queries with that node's
// own tailnet IP. The apex `<community.domain>` returns NXDOMAIN; names
// outside the configured zones return REFUSED.
//
// Build into Caddy via xcaddy:
//
//	xcaddy build --with github.com/tailnet-bridge/wildcard/caddy-plugin/bridgedns
package bridgedns

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(&App{})
}

// App is the Caddy app module.
//
// JSON shape:
//
//	{
//	  "nodes": {
//	    "<community-id>": {
//	      "tsnet_node": "personal-<community-id>",
//	      "domain":     "<community>.ts.<base>",
//	      "port":       53
//	    }
//	  }
//	}
type App struct {
	Nodes map[string]Node `json:"nodes,omitempty"`

	ctx     caddy.Context
	logger  *zap.Logger
	servers []*runningServer
}

// Node describes one community's DNS zone.
type Node struct {
	// TsnetNode is the name of the caddy-tailscale node to bind to
	// (must match an entry in apps.tailscale.nodes).
	TsnetNode string `json:"tsnet_node"`

	// Domain is the community subdomain whose subtree this responder
	// owns — e.g. "smithfamily.ts.example.com".
	Domain string `json:"domain"`

	// Port to bind. 0 means 53.
	Port uint16 `json:"port,omitempty"`
}

type runningServer struct {
	id       string
	domain   string
	port     uint16
	pc       net.PacketConn
	addr     netip.Addr
	srv      *dns.Server
	stopOnce sync.Once
}

// CaddyModule registers this app with Caddy.
func (*App) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "bridgedns",
		New: func() caddy.Module { return new(App) },
	}
}

// Provision is called by Caddy after config is loaded.
func (a *App) Provision(ctx caddy.Context) error {
	a.ctx = ctx
	a.logger = ctx.Logger()
	return nil
}

// Start brings up one DNS server per configured community.
//
// We use caddy.ListenPacket on the `tailscale/udp` registered network
// (provided by github.com/tailscale/caddy-tailscale). This shares the
// underlying tsnet.Server with whichever HTTPS listener already
// references the same node — the design hinge for the wildcard-bridge.
func (a *App) Start() error {
	for id, n := range a.Nodes {
		port := n.Port
		if port == 0 {
			port = 53
		}
		if n.TsnetNode == "" {
			return fmt.Errorf("bridgedns: %s: tsnet_node is required", id)
		}
		if n.Domain == "" {
			return fmt.Errorf("bridgedns: %s: domain is required", id)
		}
		combined := "tailscale/udp/" + n.TsnetNode + ":" + strconv.Itoa(int(port))
		na, err := caddy.ParseNetworkAddress(combined)
		if err != nil {
			return fmt.Errorf("bridgedns: %s: parse %s: %w", id, combined, err)
		}
		lnAny, err := na.Listen(a.ctx, 0, net.ListenConfig{})
		if err != nil {
			return fmt.Errorf("bridgedns: %s: listen %s: %w", id, combined, err)
		}
		pc, ok := lnAny.(net.PacketConn)
		if !ok {
			return fmt.Errorf("bridgedns: %s: listener type %T is not net.PacketConn", id, lnAny)
		}

		ourIP, err := localIP(pc)
		if err != nil {
			_ = pc.Close()
			return fmt.Errorf("bridgedns: %s: %w", id, err)
		}

		rs := &runningServer{
			id:     id,
			domain: strings.ToLower(strings.TrimSuffix(n.Domain, ".")),
			port:   port,
			pc:     pc,
			addr:   ourIP,
		}
		rs.srv = &dns.Server{
			PacketConn: pc,
			Handler:    dns.HandlerFunc(rs.handle),
		}
		go func() {
			if err := rs.srv.ActivateAndServe(); err != nil && !isClosed(err) {
				a.logger.Error("bridgedns: server exited",
					zap.String("community", rs.id),
					zap.Error(err))
			}
		}()
		a.servers = append(a.servers, rs)
		a.logger.Info("bridgedns: listening",
			zap.String("community", rs.id),
			zap.String("domain", rs.domain),
			zap.Stringer("ip", rs.addr),
			zap.Uint16("port", rs.port),
		)
	}
	return nil
}

// Stop tears down every running DNS server.
func (a *App) Stop() error {
	var firstErr error
	for _, rs := range a.servers {
		rs.stopOnce.Do(func() {
			if err := rs.srv.Shutdown(); err != nil && firstErr == nil {
				firstErr = err
			}
		})
	}
	a.servers = nil
	return firstErr
}

// handle answers one DNS message.
//
// Behaviour (SPEC §7.3, §8.2, §10.1):
//   - `*.<domain>` with exactly one label below the apex (A/AAAA): the
//     node's own tailnet IP. Matches the wildcard cert coverage.
//   - `<domain>` (apex): NXDOMAIN.
//   - Two or more labels below the apex: NXDOMAIN (the wildcard cert
//     does not cover those names, and SPEC §8.2 forbids dotted service
//     names).
//   - Anything outside `<domain>`: REFUSED.
//   - Malformed: drop silently.
func (rs *runningServer) handle(w dns.ResponseWriter, r *dns.Msg) {
	if r == nil || len(r.Question) == 0 {
		return
	}
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = false

	q := r.Question[0]
	qname := strings.ToLower(strings.TrimSuffix(q.Name, "."))
	suffix := "." + rs.domain

	switch {
	case qname == rs.domain:
		// Apex: NXDOMAIN. Only children exist.
		m.Rcode = dns.RcodeNameError
	case strings.HasSuffix(qname, suffix):
		// Subdomain: only ONE label below the apex is in zone.
		head := qname[:len(qname)-len(suffix)]
		if head == "" || strings.ContainsRune(head, '.') {
			m.Rcode = dns.RcodeNameError
			break
		}
		switch q.Qtype {
		case dns.TypeA:
			if rs.addr.Is4() {
				rr, _ := dns.NewRR(q.Name + " 60 IN A " + rs.addr.String())
				if rr != nil {
					m.Answer = append(m.Answer, rr)
				}
			}
			// If we only have IPv6, return empty A response (NOERROR, no records).
		case dns.TypeAAAA:
			if rs.addr.Is6() {
				rr, _ := dns.NewRR(q.Name + " 60 IN AAAA " + rs.addr.String())
				if rr != nil {
					m.Answer = append(m.Answer, rr)
				}
			}
		case dns.TypeANY, dns.TypeHTTPS, dns.TypeSVCB:
			if rs.addr.Is4() {
				rr, _ := dns.NewRR(q.Name + " 60 IN A " + rs.addr.String())
				if rr != nil {
					m.Answer = append(m.Answer, rr)
				}
			} else {
				rr, _ := dns.NewRR(q.Name + " 60 IN AAAA " + rs.addr.String())
				if rr != nil {
					m.Answer = append(m.Answer, rr)
				}
			}
		default:
			// Other qtypes: NOERROR, no records.
		}
	default:
		m.Rcode = dns.RcodeRefused
	}
	_ = w.WriteMsg(m)
}

// localIP extracts the bound tailnet IP from a PacketConn's LocalAddr.
// The address returned by caddy-tailscale's getUDPListener is the tsnet
// node's tailnet AddrPort.
func localIP(pc net.PacketConn) (netip.Addr, error) {
	la := pc.LocalAddr()
	if la == nil {
		return netip.Addr{}, errors.New("bridgedns: nil LocalAddr")
	}
	// Try UDPAddr first (the normal case for net.PacketConn).
	if u, ok := la.(*net.UDPAddr); ok {
		if a, ok := netip.AddrFromSlice(u.IP); ok {
			return a.Unmap(), nil
		}
	}
	// Fallback: parse the string form (Go versions where tsnet wraps
	// the addr differently).
	host, _, err := net.SplitHostPort(la.String())
	if err != nil {
		return netip.Addr{}, fmt.Errorf("split %q: %w", la.String(), err)
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse %q: %w", host, err)
	}
	return a, nil
}

// isClosed reports whether err is a "use of closed network connection"
// signal — expected on Stop, not worth logging as an error.
func isClosed(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "use of closed network connection") ||
		errors.Is(err, net.ErrClosed)
}

var (
	_ caddy.App         = (*App)(nil)
	_ caddy.Provisioner = (*App)(nil)
)
