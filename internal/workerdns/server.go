package workerdns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const defaultForwardTimeout = 2 * time.Second

type InternalResolver interface {
	Resolve(ctx context.Context, sourceIP netip.Addr, qname string, qtype uint16) (Resolution, error)
}

type Config struct {
	Addr                string
	AllowedClientPrefix netip.Prefix
	Upstreams           []string
	ForwardTimeout      time.Duration
}

type Server struct {
	cfg      Config
	resolver InternalResolver
	udpConn  net.PacketConn
	tcpLn    net.Listener
	udpSrv   *dns.Server
	tcpSrv   *dns.Server
}

func NewServer(cfg Config, resolver InternalResolver) *Server {
	if strings.TrimSpace(cfg.Addr) == "" {
		cfg.Addr = ":53"
	}
	if cfg.ForwardTimeout <= 0 {
		cfg.ForwardTimeout = defaultForwardTimeout
	}
	cfg.Upstreams = normalizeUpstreams(cfg.Upstreams)
	return &Server{cfg: cfg, resolver: resolver}
}

func (s *Server) Start(ctx context.Context) error {
	udpConn, err := net.ListenPacket("udp", s.cfg.Addr)
	if err != nil {
		return err
	}
	tcpLn, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		_ = udpConn.Close()
		return err
	}

	s.udpConn = udpConn
	s.tcpLn = tcpLn
	s.udpSrv = &dns.Server{PacketConn: udpConn, Handler: s}
	s.tcpSrv = &dns.Server{Listener: tcpLn, Handler: s}

	go func() {
		<-ctx.Done()
		_ = s.udpSrv.Shutdown()
		_ = s.tcpSrv.Shutdown()
	}()
	go func() {
		_ = s.udpSrv.ActivateAndServe()
	}()
	go func() {
		_ = s.tcpSrv.ActivateAndServe()
	}()
	return nil
}

func (s *Server) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	reply := new(dns.Msg)
	reply.SetReply(req)

	sourceIP, ok := remoteAddrIP(w.RemoteAddr())
	if !ok || (s.cfg.AllowedClientPrefix.IsValid() && !s.cfg.AllowedClientPrefix.Contains(sourceIP)) {
		reply.Rcode = dns.RcodeRefused
		_ = w.WriteMsg(reply)
		return
	}
	if len(req.Question) == 0 {
		reply.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(reply)
		return
	}

	question := req.Question[0]
	resolution, err := s.resolver.Resolve(context.Background(), sourceIP, question.Name, question.Qtype)
	if err != nil {
		reply.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(reply)
		return
	}
	if resolution.Handled {
		reply.Authoritative = true
		reply.Rcode = resolution.Rcode
		reply.Answer = resolution.Answers
		_ = w.WriteMsg(reply)
		return
	}

	resp, err := s.forward(req, networkForRemoteAddr(w.RemoteAddr()))
	if err != nil {
		reply.Rcode = dns.RcodeServerFailure
		_ = w.WriteMsg(reply)
		return
	}
	resp.Id = req.Id
	_ = w.WriteMsg(resp)
}

func (s *Server) forward(req *dns.Msg, network string) (*dns.Msg, error) {
	if len(s.cfg.Upstreams) == 0 {
		return nil, errors.New("no upstream resolvers configured")
	}
	client := &dns.Client{
		Net:     network,
		Timeout: s.cfg.ForwardTimeout,
	}
	var lastErr error
	for _, upstream := range s.cfg.Upstreams {
		resp, _, err := client.Exchange(req, upstream)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("forward query failed")
	}
	return nil, lastErr
}

func remoteAddrIP(addr net.Addr) (netip.Addr, bool) {
	switch v := addr.(type) {
	case *net.UDPAddr:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip.Unmap(), true
		}
	case *net.TCPAddr:
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip.Unmap(), true
		}
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err == nil {
			if ip, err := netip.ParseAddr(host); err == nil {
				return ip.Unmap(), true
			}
		}
	}
	return netip.Addr{}, false
}

func networkForRemoteAddr(addr net.Addr) string {
	switch addr.(type) {
	case *net.TCPAddr:
		return "tcp"
	default:
		return "udp"
	}
}

func normalizeUpstreams(raw []string) []string {
	if len(raw) == 0 {
		raw = []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	out := make([]string, 0, len(raw))
	for _, upstream := range raw {
		upstream = strings.TrimSpace(upstream)
		if upstream == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(upstream); err != nil {
			upstream = net.JoinHostPort(upstream, "53")
		}
		out = append(out, upstream)
	}
	return out
}
