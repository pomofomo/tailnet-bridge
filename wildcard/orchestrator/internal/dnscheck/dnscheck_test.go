package dnscheck

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// startMockResolver spins up a miekg/dns UDP server on 127.0.0.1:0 and
// returns its address. respond decides what to answer for a question.
func startMockResolver(t *testing.T, respond func(q dns.Question, m *dns.Msg)) (addr string, stop func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		if len(r.Question) > 0 {
			respond(r.Question[0], m)
		}
		_ = w.WriteMsg(m)
	})}
	go func() { _ = srv.ActivateAndServe() }()
	return pc.LocalAddr().String(), func() {
		_ = srv.Shutdown()
		_ = pc.Close()
	}
}

func TestCheckOnce_NXDOMAIN_NotViolation(t *testing.T) {
	addr, stop := startMockResolver(t, func(q dns.Question, m *dns.Msg) {
		m.Rcode = dns.RcodeNameError
	})
	defer stop()

	c := &Checker{Resolver: addr}
	res := c.CheckOnce(context.Background(), "smith.ts.example.com")
	if res.Violation {
		t.Errorf("NXDOMAIN should not be a violation: %+v", res)
	}
	if res.Err != nil {
		t.Errorf("NXDOMAIN should not produce Err: %v", res.Err)
	}
}

func TestCheckOnce_PositiveAnswer_IsViolation(t *testing.T) {
	addr, stop := startMockResolver(t, func(q dns.Question, m *dns.Msg) {
		if q.Qtype == dns.TypeA {
			rr, _ := dns.NewRR(q.Name + " 60 IN A 203.0.113.42")
			m.Answer = append(m.Answer, rr)
		}
	})
	defer stop()

	c := &Checker{Resolver: addr}
	res := c.CheckOnce(context.Background(), "smith.ts.example.com")
	if !res.Violation {
		t.Errorf("positive answer must be a violation: %+v", res)
	}
	if len(res.Answers) == 0 {
		t.Error("answers not recorded")
	}
}

func TestRun_FiresOnResultPerDomain(t *testing.T) {
	addr, stop := startMockResolver(t, func(q dns.Question, m *dns.Msg) {
		m.Rcode = dns.RcodeNameError
	})
	defer stop()

	var (
		mu  sync.Mutex
		got []string
	)
	c := &Checker{
		Resolver: addr,
		Domains:  []string{"smith.ts.example.com", "austin.ts.example.com"},
		Interval: 0,
		OnResult: func(r Result) {
			mu.Lock()
			got = append(got, r.Domain)
			mu.Unlock()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// 2 domains × 2 probes (domain + any.domain) = 4.
	if len(got) != 4 {
		t.Errorf("expected 4 results, got %d: %v", len(got), got)
	}
}
