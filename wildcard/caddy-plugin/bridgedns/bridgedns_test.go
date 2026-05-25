package bridgedns

import (
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
)

type captureWriter struct {
	got *dns.Msg
}

func (c *captureWriter) LocalAddr() net.Addr         { return &net.UDPAddr{} }
func (c *captureWriter) RemoteAddr() net.Addr        { return &net.UDPAddr{} }
func (c *captureWriter) WriteMsg(m *dns.Msg) error   { c.got = m; return nil }
func (c *captureWriter) Write(_ []byte) (int, error) { return 0, nil }
func (c *captureWriter) Close() error                { return nil }
func (c *captureWriter) TsigStatus() error           { return nil }
func (c *captureWriter) TsigTimersOnly(bool)         {}
func (c *captureWriter) Hijack()                     {}

func newRS(addr netip.Addr) *runningServer {
	return &runningServer{
		id:     "smith",
		domain: "smith.ts.example.com",
		addr:   addr,
	}
}

func ask(t *testing.T, rs *runningServer, qname string, qtype uint16) *dns.Msg {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(qname), qtype)
	w := &captureWriter{}
	rs.handle(w, q)
	if w.got == nil {
		t.Fatalf("no response written")
	}
	return w.got
}

func TestHandle_WildcardA(t *testing.T) {
	rs := newRS(netip.MustParseAddr("100.64.0.42"))
	resp := ask(t, rs, "wiki.smith.ts.example.com", dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode: %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answers: %d", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("not A: %T", resp.Answer[0])
	}
	if a.A.String() != "100.64.0.42" {
		t.Errorf("wrong IP: %s", a.A)
	}
}

func TestHandle_WildcardAAAA_IPv4Only_ReturnsEmpty(t *testing.T) {
	rs := newRS(netip.MustParseAddr("100.64.0.42"))
	resp := ask(t, rs, "wiki.smith.ts.example.com", dns.TypeAAAA)
	if resp.Rcode != dns.RcodeSuccess {
		t.Errorf("rcode: %d", resp.Rcode)
	}
	if len(resp.Answer) != 0 {
		t.Errorf("AAAA should be empty when only IPv4 bound: %v", resp.Answer)
	}
}

func TestHandle_IPv6Bound(t *testing.T) {
	rs := newRS(netip.MustParseAddr("fd7a:115c:a1e0::42"))
	resp := ask(t, rs, "wiki.smith.ts.example.com", dns.TypeAAAA)
	if len(resp.Answer) != 1 {
		t.Fatalf("answers: %d", len(resp.Answer))
	}
	if _, ok := resp.Answer[0].(*dns.AAAA); !ok {
		t.Errorf("not AAAA: %T", resp.Answer[0])
	}
}

func TestHandle_ApexNXDomain(t *testing.T) {
	rs := newRS(netip.MustParseAddr("100.64.0.42"))
	resp := ask(t, rs, "smith.ts.example.com", dns.TypeA)
	if resp.Rcode != dns.RcodeNameError {
		t.Errorf("apex should NXDOMAIN, got rcode %d", resp.Rcode)
	}
}

func TestHandle_OutsideZoneRefused(t *testing.T) {
	rs := newRS(netip.MustParseAddr("100.64.0.42"))
	resp := ask(t, rs, "elsewhere.example.com", dns.TypeA)
	if resp.Rcode != dns.RcodeRefused {
		t.Errorf("out-of-zone should REFUSED, got rcode %d", resp.Rcode)
	}
}

func TestHandle_AnswerAuthoritative(t *testing.T) {
	rs := newRS(netip.MustParseAddr("100.64.0.42"))
	resp := ask(t, rs, "wiki.smith.ts.example.com", dns.TypeA)
	if !resp.Authoritative {
		t.Error("response should be authoritative")
	}
	if resp.RecursionAvailable {
		t.Error("RA should be false")
	}
}

func TestHandle_CaseInsensitive(t *testing.T) {
	rs := newRS(netip.MustParseAddr("100.64.0.42"))
	resp := ask(t, rs, "WIKI.Smith.ts.example.com", dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Errorf("case folding broke: %+v", resp)
	}
}
