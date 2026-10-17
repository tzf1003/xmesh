package mdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

const (
	announceInterval = 10 * time.Second
	maxPacket        = 9000
)

// listenMulticast binds the mDNS port, joins the group on iface and pins the multicast egress to it.
// Binding the group address makes the standard library share the port with other mDNS stacks on the host.
func listenMulticast(iface net.Interface) (*ipv4.PacketConn, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: groupIP, Port: port})
	if err != nil {
		return nil, fmt.Errorf("mDNS 绑定 UDP %d 失败: %w", port, err)
	}
	p := ipv4.NewPacketConn(c)
	if err = p.JoinGroup(&iface, &net.UDPAddr{IP: groupIP}); err != nil {
		c.Close()
		return nil, fmt.Errorf("mDNS 在接口 %s 加入组播组失败: %w", iface.Name, err)
	}
	if err = pinEgress(p, iface); err != nil {
		c.Close()
		return nil, err
	}
	return p, nil
}

// listenQuery opens the socket queries leave from: an ephemeral port on the interface's own address, so
// responders see a source port other than 5353 and answer by unicast.
func listenQuery(iface net.Interface, ip net.IP) (*ipv4.PacketConn, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if err != nil {
		return nil, fmt.Errorf("mDNS 查询端口绑定 %s 失败: %w", ip, err)
	}
	p := ipv4.NewPacketConn(c)
	if err = pinEgress(p, iface); err != nil {
		c.Close()
		return nil, err
	}
	return p, nil
}

func pinEgress(p *ipv4.PacketConn, iface net.Interface) error {
	if err := p.SetMulticastInterface(&iface); err != nil {
		return fmt.Errorf("mDNS 固定组播出口接口 %s 失败: %w", iface.Name, err)
	}
	_ = p.SetMulticastTTL(255)
	_ = p.SetMulticastLoopback(true)
	return nil
}

type packet struct {
	msg  *dns.Msg
	from *net.UDPAddr
}

// readLoop parses datagrams until the connection is closed. Other read errors are not fatal: Windows
// reports the ICMP port-unreachable of an earlier unicast reply as a failed read of the next datagram.
func readLoop(p *ipv4.PacketConn, deliver func(packet)) {
	buf := make([]byte, maxPacket)
	failures := 0
	for {
		n, _, from, err := p.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if failures++; failures > 100 {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		failures = 0
		src, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		msg := new(dns.Msg)
		if msg.Unpack(buf[:n]) != nil {
			continue
		}
		deliver(packet{msg: msg, from: src})
	}
}

func send(p *ipv4.PacketConn, msg *dns.Msg, to *net.UDPAddr) {
	if buf, err := msg.Pack(); err == nil {
		_, _ = p.WriteTo(buf, nil, to)
	}
}

// Responder publishes one service and answers queries for it.
type Responder struct {
	svc    Service
	conn   *ipv4.PacketConn
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// Advertise publishes svc on iface until Close. It announces at start, again after one and three
// seconds, then every few seconds, and says goodbye on Close.
func Advertise(svc Service, iface net.Interface) (*Responder, error) {
	if svc.IP.To4() == nil {
		return nil, fmt.Errorf("mDNS 需要 IPv4 地址: %v", svc.IP)
	}
	if svc.Instance == "" || svc.Type == "" || svc.Port <= 0 {
		return nil, errors.New("mDNS 服务缺少实例名、类型或端口")
	}
	conn, err := listenMulticast(iface)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Responder{svc: svc, conn: conn, cancel: cancel}
	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		readLoop(conn, r.handle)
	}()
	go func() {
		defer r.wg.Done()
		r.announceLoop(ctx)
	}()
	return r, nil
}

func (r *Responder) handle(p packet) {
	if p.msg.Response || len(p.msg.Question) == 0 {
		return
	}
	resp, unicast := r.svc.reply(p.msg, p.from.Port != port)
	if resp == nil {
		return
	}
	if unicast {
		send(r.conn, resp, p.from)
		return
	}
	send(r.conn, resp, groupAddr)
}

func (r *Responder) announceLoop(ctx context.Context) {
	delays := []time.Duration{0, time.Second, 2 * time.Second}
	for i := 0; ; i++ {
		delay := announceInterval
		if i < len(delays) {
			delay = delays[i]
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		send(r.conn, r.svc.announcement(recordTTL), groupAddr)
	}
}

// Close withdraws the service and releases the socket.
func (r *Responder) Close() {
	r.once.Do(func() {
		r.cancel()
		goodbye := r.svc.announcement(0)
		send(r.conn, goodbye, groupAddr)
		send(r.conn, goodbye, groupAddr)
		r.conn.Close()
		r.wg.Wait()
	})
}

// querySchedule are the offsets, from the start of a browse, at which the PTR question is sent.
var querySchedule = []time.Duration{0, time.Second, 3 * time.Second, 6 * time.Second}

// Browse looks for instances of serviceType on iface until ctx ends, then closes the returned channel.
// ip is the interface's IPv4 address, used as the source of the queries. Each instance is delivered
// once it is fully resolved, or with a zero TTL when it says goodbye.
func Browse(ctx context.Context, serviceType, domain string, iface net.Interface, ip net.IP) (<-chan *Entry, error) {
	if ip.To4() == nil {
		return nil, fmt.Errorf("mDNS 需要 IPv4 地址: %v", ip)
	}
	listener, err := listenMulticast(iface)
	if err != nil {
		return nil, err
	}
	asker, err := listenQuery(iface, ip)
	if err != nil {
		listener.Close()
		return nil, err
	}
	svc := Service{Type: serviceType, Domain: domain}
	question := new(dns.Msg)
	question.SetQuestion(svc.serviceName(), dns.TypePTR)
	question.RecursionDesired = false

	out := make(chan *Entry, 32)
	packets := make(chan packet, 32)
	var readers sync.WaitGroup
	for _, conn := range []*ipv4.PacketConn{listener, asker} {
		readers.Add(1)
		go func() {
			defer readers.Done()
			readLoop(conn, func(p packet) {
				select {
				case packets <- p:
				case <-ctx.Done():
				}
			})
		}()
	}
	go func() {
		defer close(out)
		defer readers.Wait()
		defer listener.Close()
		defer asker.Close()
		found := newCollector(serviceType, domain)
		start := time.Now()
		next := 0
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				send(asker, question, groupAddr)
				if next++; next < len(querySchedule) {
					timer.Reset(time.Until(start.Add(querySchedule[next])))
				}
			case p := <-packets:
				for _, entry := range found.add(p.msg) {
					select {
					case out <- entry:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out, nil
}
