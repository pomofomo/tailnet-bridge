// Package dns implements the embedded UDP/53 responder that the user's
// personal-tailnet Split DNS configuration points at (SPEC §7.3, §10.1).
//
// For each community, the responder answers queries for
// "*.<community.domain>" with the personal-tailnet IP of that community's
// listener tsnet node. Every other name receives REFUSED.
//
// Status: STUB.
package dns

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

// Zone is one community's DNS configuration.
type Zone struct {
	// Domain is the community subdomain whose subtree this responder
	// owns — e.g. "smithfamily.ts.example.com".
	Domain string

	// ListenerIP is the personal-tailnet IP that *.Domain should
	// resolve to. This is the IP the tsnet listener node for this
	// community came up on.
	ListenerIP netip.Addr
}

// Server answers DNS queries for one or more community zones.
//
// One Server instance can host multiple zones; it's the orchestrator's
// choice whether to run one Server per community (bound to each
// listener node's tailnet IP, port 53) or one Server total. SPEC §10.1
// suggests per-community, since Tailscale Split DNS routes by domain.
type Server struct {
	// Listener is the bound UDP socket. Provided by the caller so the
	// orchestrator can bind it to a specific tailnet interface IP.
	Listener net.PacketConn

	Zones []Zone
}

// Serve blocks reading queries off s.Listener and answering them until
// ctx is cancelled.
//
// Behavior:
//   - QTYPE A and AAAA for "*.<Domain>": answer with s.Zones[i].ListenerIP
//     (A or AAAA depending on the address family).
//   - QTYPE ANY for "*.<Domain>": answer with whatever family the
//     listener has.
//   - QTYPE for "<Domain>" itself: NXDOMAIN (only subdomains exist).
//   - Anything outside any configured zone: REFUSED.
//   - Anything malformed: drop silently (don't amplify).
//
// Implementation note: use github.com/miekg/dns (already a transitive
// dep of tsnet) so we don't hand-roll wire-format parsing.
func (s *Server) Serve(ctx context.Context) error {
	// TODO(impl): see the behavior list above.
	_ = ctx
	return errNotImplemented
}

// Update atomically swaps the zone set. Safe to call from another
// goroutine while Serve is running — used by the poller when a
// community is added/removed/refreshed.
func (s *Server) Update(zones []Zone) {
	// TODO(impl): protect Zones with a mutex; or store an atomic.Value.
	_ = zones
}

var errNotImplemented = errors.New("dns: not yet implemented")
