// Package mdns is the small multicast-DNS responder and browser behind LAN discovery.
//
// It speaks IPv4 on exactly one interface and keeps every datagram on it: the multicast egress
// interface is pinned with IP_MULTICAST_IF, which matters on Windows, where the per-packet interface
// of a control message is ignored and the stack otherwise picks the egress by route metric (a Clash or
// Tailscale TUN adapter often wins that). Queries leave from an ephemeral UDP port, so every responder
// answers them by unicast (RFC 6762 section 6.7) and discovery does not depend on multicast working
// in the direction responder -> querier. Responders also re-announce periodically so passive
// listeners keep the presence of a node fresh.
package mdns

import (
	"net"
	"strings"

	"github.com/miekg/dns"
)

const (
	port = 5353
	// recordTTL and addressTTL are the lifetimes, in seconds, of the service and address records.
	recordTTL  = 120
	addressTTL = 120
	// cacheFlush is the top bit of the class of a unique record (RFC 6762 section 10.2); the same
	// bit in a question asks for a unicast answer (section 5.4).
	cacheFlush = 1 << 15
)

var (
	groupIP   = net.IPv4(224, 0, 0, 251)
	groupAddr = &net.UDPAddr{IP: groupIP, Port: port}
)

// Service is one DNS-SD service instance published on one IPv4 address.
type Service struct {
	Instance string // instance label, e.g. "mac-lan"
	Type     string // service type, e.g. "_xmesh._tcp"
	Domain   string // empty means "local"
	Host     string // host label, e.g. "mac-lan"; the domain is appended
	Port     int
	Text     []string
	IP       net.IP
}

// Entry is a service instance learned from the network. TTL is zero for a goodbye.
type Entry struct {
	Instance string
	HostName string
	Port     int
	Text     []string
	TTL      uint32
	AddrIPv4 []net.IP
}

// Label turns a free-form node name into one valid DNS host label: letters, digits and hyphens.
func Label(name string) string {
	label := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		}
		return '-'
	}, name)
	if label = strings.Trim(label, "-"); label == "" {
		return "xmesh"
	}
	if len(label) > 63 {
		label = strings.TrimRight(label[:63], "-")
	}
	return label
}

func (s Service) domain() string {
	if d := strings.Trim(s.Domain, "."); d != "" {
		return d
	}
	return "local"
}

func (s Service) serviceName() string { return strings.Trim(s.Type, ".") + "." + s.domain() + "." }

// instanceName escapes the two characters that would split the label.
func (s Service) instanceName() string {
	return strings.NewReplacer(".", "-", `\`, "-").Replace(s.Instance) + "." + s.serviceName()
}

func (s Service) hostName() string { return Label(s.Host) + "." + s.domain() + "." }

func (s Service) metaName() string { return "_services._dns-sd._udp." + s.domain() + "." }

func canon(name string) string { return strings.ToLower(dns.Fqdn(name)) }

// records builds the records of the service; a ttl of zero is the goodbye form.
func (s Service) records(ttl uint32) (ptr *dns.PTR, srv *dns.SRV, txt *dns.TXT, a *dns.A) {
	text := s.Text
	if len(text) == 0 {
		text = []string{""}
	}
	addressLife := uint32(addressTTL)
	if ttl == 0 {
		addressLife = 0
	}
	instance := s.instanceName()
	ptr = &dns.PTR{Hdr: dns.RR_Header{Name: s.serviceName(), Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: ttl}, Ptr: instance}
	srv = &dns.SRV{Hdr: dns.RR_Header{Name: instance, Rrtype: dns.TypeSRV, Class: dns.ClassINET | cacheFlush, Ttl: ttl}, Port: uint16(s.Port), Target: s.hostName()}
	txt = &dns.TXT{Hdr: dns.RR_Header{Name: instance, Rrtype: dns.TypeTXT, Class: dns.ClassINET | cacheFlush, Ttl: ttl}, Txt: text}
	a = &dns.A{Hdr: dns.RR_Header{Name: s.hostName(), Rrtype: dns.TypeA, Class: dns.ClassINET | cacheFlush, Ttl: addressLife}, A: s.IP.To4()}
	return
}

// announcement is the unsolicited multicast response sent at start, periodically and, with a zero ttl,
// when the service goes away.
func (s Service) announcement(ttl uint32) *dns.Msg {
	ptr, srv, txt, a := s.records(ttl)
	m := new(dns.Msg)
	m.Response, m.Authoritative = true, true
	m.Answer = []dns.RR{ptr, srv, txt, a}
	return m
}

// answer returns what the service says to one question.
func (s Service) answer(q dns.Question) (answer, extra []dns.RR) {
	ptr, srv, txt, a := s.records(recordTTL)
	name, any := canon(q.Name), q.Qtype == dns.TypeANY
	switch name {
	case canon(s.metaName()):
		if q.Qtype == dns.TypePTR || any {
			answer = []dns.RR{&dns.PTR{Hdr: dns.RR_Header{Name: s.metaName(), Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: recordTTL}, Ptr: s.serviceName()}}
		}
	case canon(s.serviceName()):
		if q.Qtype == dns.TypePTR || any {
			answer, extra = []dns.RR{ptr}, []dns.RR{srv, txt, a}
		}
	case canon(s.instanceName()):
		switch {
		case any:
			answer, extra = []dns.RR{srv, txt}, []dns.RR{a}
		case q.Qtype == dns.TypeSRV:
			answer, extra = []dns.RR{srv}, []dns.RR{a}
		case q.Qtype == dns.TypeTXT:
			answer, extra = []dns.RR{txt}, []dns.RR{a}
		}
	case canon(s.hostName()):
		if q.Qtype == dns.TypeA || any {
			answer = []dns.RR{a}
		}
	}
	return
}

// reply answers a query message. The second result says whether it must go to the sender by unicast:
// the sender asked for it (QU bit) or is a one-shot resolver that does not listen on port 5353.
// It returns nil when nothing in the query concerns this service.
func (s Service) reply(query *dns.Msg, legacy bool) (*dns.Msg, bool) {
	var answer, extra []dns.RR
	unicast := legacy
	seen := map[string]bool{}
	add := func(dst *[]dns.RR, rrs []dns.RR) {
		for _, rr := range rrs {
			if key := rr.String(); !seen[key] {
				seen[key] = true
				*dst = append(*dst, rr)
			}
		}
	}
	for _, q := range query.Question {
		a, e := s.answer(q)
		if len(a) == 0 {
			continue
		}
		unicast = unicast || q.Qclass&cacheFlush != 0
		add(&answer, a)
		add(&extra, e)
	}
	if len(answer) == 0 {
		return nil, false
	}
	resp := new(dns.Msg)
	resp.Response, resp.Authoritative, resp.Compress = true, true, true
	if legacy {
		// A legacy resolver matches the answer to its question by id and question.
		resp.Id, resp.Question = query.Id, query.Question
	}
	resp.Answer, resp.Extra = answer, extra
	return resp, unicast
}

// collector folds the records of many messages into complete entries. The records of one instance
// often arrive in several datagrams, so state is kept across messages.
type collector struct {
	service   string
	instances map[string]*instance
	hosts     map[string][]net.IP
}

type instance struct {
	entry     Entry
	hasSRV    bool
	hasTXT    bool
	goodbye   bool
	delivered bool
}

func newCollector(serviceType, domain string) *collector {
	if domain = strings.Trim(domain, "."); domain == "" {
		domain = "local"
	}
	return &collector{service: canon(strings.Trim(serviceType, ".") + "." + domain), instances: map[string]*instance{}, hosts: map[string][]net.IP{}}
}

func (c *collector) instance(name string) *instance {
	key := canon(name)
	if i := c.instances[key]; i != nil {
		return i
	}
	i := &instance{}
	label, suffix := strings.TrimSuffix(name, "."), "."+strings.TrimSuffix(c.service, ".")
	if n := len(label) - len(suffix); n > 0 && strings.EqualFold(label[n:], suffix) {
		label = label[:n]
	}
	i.entry.Instance = label
	c.instances[key] = i
	return i
}

// add folds one message in and returns the entries it completed (or withdrew with a goodbye).
func (c *collector) add(msg *dns.Msg) []*Entry {
	if !msg.Response {
		return nil
	}
	rrs := append(append(append([]dns.RR{}, msg.Answer...), msg.Ns...), msg.Extra...)
	touched := map[string]*instance{}
	for _, rr := range rrs {
		hdr := rr.Header()
		name := canon(hdr.Name)
		switch rr := rr.(type) {
		case *dns.PTR:
			if name != c.service {
				continue
			}
			i := c.instance(rr.Ptr)
			i.entry.TTL, i.goodbye = rr.Hdr.Ttl, i.goodbye || rr.Hdr.Ttl == 0
			touched[canon(rr.Ptr)] = i
		case *dns.SRV:
			if !strings.HasSuffix(name, "."+c.service) {
				continue
			}
			i := c.instance(rr.Hdr.Name)
			i.entry.HostName, i.entry.Port, i.hasSRV = rr.Target, int(rr.Port), true
			i.entry.TTL, i.goodbye = rr.Hdr.Ttl, i.goodbye || rr.Hdr.Ttl == 0
			touched[name] = i
		case *dns.TXT:
			if !strings.HasSuffix(name, "."+c.service) {
				continue
			}
			i := c.instance(rr.Hdr.Name)
			i.entry.Text, i.hasTXT = rr.Txt, true
			i.entry.TTL, i.goodbye = rr.Hdr.Ttl, i.goodbye || rr.Hdr.Ttl == 0
			touched[name] = i
		case *dns.A:
			ip := rr.A.To4()
			if ip == nil || rr.Hdr.Ttl == 0 {
				continue
			}
			if !contains(c.hosts[name], ip) {
				c.hosts[name] = append(c.hosts[name], ip)
			}
			// an address can complete an instance whose SRV arrived in an earlier datagram
			for key, i := range c.instances {
				if i.hasSRV && canon(i.entry.HostName) == name {
					touched[key] = i
				}
			}
		}
	}
	var out []*Entry
	for key, i := range touched {
		if i.goodbye {
			// A goodbye is final for this announcement; a later announcement starts the node afresh.
			e := i.entry
			e.TTL, e.AddrIPv4 = 0, nil
			delete(c.instances, key)
			out = append(out, &e)
			continue
		}
		if i.delivered || !i.hasSRV || !i.hasTXT {
			continue
		}
		ips := c.hosts[canon(i.entry.HostName)]
		if len(ips) == 0 {
			continue
		}
		i.entry.AddrIPv4, i.delivered = append([]net.IP(nil), ips...), true
		e := i.entry
		out = append(out, &e)
	}
	return out
}

func contains(ips []net.IP, ip net.IP) bool {
	for _, have := range ips {
		if have.Equal(ip) {
			return true
		}
	}
	return false
}
