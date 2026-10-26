package connectivity

import (
	"context"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/cfpreferred"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
)

func addrs(t *testing.T, in []netip.Addr) string {
	t.Helper()
	s := make([]string, len(in))
	for i, a := range in {
		s[i] = a.String()
	}
	return strings.Join(s, ",")
}

func TestPoolOrdersByMeasuredLatencyAndCoolsFailures(t *testing.T) {
	p := newPreferredPool("")
	clock := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }
	cfg := config.Cloudflare{PreferredIPs: []string{"104.16.0.1", "104.16.0.2", "104.16.0.3"}}
	if got := addrs(t, p.dialOrder(cfg, "smart", nil)); got != "104.16.0.1,104.16.0.2,104.16.0.3" {
		t.Fatal("unmeasured addresses keep source order:", got)
	}
	p.st.Scan.Results = []scanRow{{IP: "104.16.0.3", LatencyMS: 90}, {IP: "104.16.0.2", LatencyMS: 200}}
	if got := addrs(t, p.dialOrder(cfg, "smart", nil)); got != "104.16.0.3,104.16.0.2,104.16.0.1" {
		t.Fatal("measured addresses go first, fastest first:", got)
	}
	for range 3 {
		p.failed(netip.MustParseAddr("104.16.0.3"), 3, 30*time.Minute)
	}
	if got := addrs(t, p.dialOrder(cfg, "smart", nil)); got != "104.16.0.2,104.16.0.1" {
		t.Fatal("a cooling address is skipped:", got)
	}
	st := p.state(cfg)
	if st.Pool[2].Status != "cooling" || st.Pool[2].CooldownUntil == nil {
		t.Fatalf("%+v", st.Pool)
	}
	clock = clock.Add(31 * time.Minute)
	if got := addrs(t, p.dialOrder(cfg, "smart", nil)); got != "104.16.0.2,104.16.0.3,104.16.0.1" {
		t.Fatal("after the cooldown the address returns, ranked behind until it succeeds:", got)
	}
	p.failed(netip.MustParseAddr("104.16.0.3"), 3, 30*time.Minute)
	if got := addrs(t, p.dialOrder(cfg, "smart", nil)); strings.Contains(got, "104.16.0.3") {
		t.Fatal("one failure after a cooldown starts the next:", got)
	}
	p.succeeded("win.example.com", netip.MustParseAddr("104.16.0.3"), 50*time.Millisecond)
	if got := addrs(t, p.dialOrder(cfg, "smart", nil)); !strings.HasPrefix(got, "104.16.0.3") {
		t.Fatal("a success clears the cooldown:", got)
	}
}

func TestPoolFixedAndOff(t *testing.T) {
	p := newPreferredPool("")
	cfg := config.Cloudflare{PreferredIPs: []string{"104.16.0.1"}}
	if got := addrs(t, p.dialOrder(cfg, "fixed", []string{"172.64.0.9", "8.8.8.8"})); got != "172.64.0.9" {
		t.Fatal(got)
	}
	if got := p.dialOrder(cfg, "off", nil); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestPoolSourcesDedupeAndDisabled(t *testing.T) {
	p := newPreferredPool("")
	file := filepath.Join(t.TempDir(), "ips")
	if err := os.WriteFile(file, []byte("104.16.0.2\n104.16.0.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.st.URLs["https://example.com/list"] = urlRecord{IPs: []string{"104.16.0.4", "104.16.0.5"}}
	p.st.Scan.Results = []scanRow{{IP: "104.16.0.6", LatencyMS: 80, Keep: true}, {IP: "104.16.0.5", LatencyMS: 70, Keep: true}}
	cfg := config.Cloudflare{PreferredIPs: []string{"104.16.0.1"}, PreferredIP: &config.PreferredIP{
		Sources: []config.PreferredSource{
			{Type: "manual", IPs: []string{"104.16.0.1", "104.16.0.3"}},
			{Type: "file", Path: file},
			{Type: "url", URL: "https://example.com/list"},
			{Type: "manual", Disabled: true, IPs: []string{"104.16.0.9"}},
		},
		Scan: &config.PreferredScan{Enabled: true},
	}}
	var got []string
	for _, m := range p.members(cfg) {
		got = append(got, m.addr.String()+":"+m.source)
	}
	want := "104.16.0.1:manual,104.16.0.3:manual,104.16.0.2:file,104.16.0.4:file,104.16.0.5:url,104.16.0.6:scan"
	if strings.Join(got, ",") != want {
		t.Fatal(strings.Join(got, ","), "!=", want)
	}
	cfg.PreferredIP.Scan.Enabled = false
	if n := len(p.members(cfg)); n != 5 {
		t.Fatal("scan results only join the pool while the scan is on:", n)
	}
}

func TestFetchURLKeepsLastGoodListOnFailure(t *testing.T) {
	good := true
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !good {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("# list\n104.16.0.1\n8.8.8.8\nnot-an-ip\n172.64.0.2\n"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	p := newPreferredPool(dir)
	p.client = srv.Client()
	p.fetchURL(context.Background(), srv.URL)
	rec := p.st.URLs[srv.URL]
	if rec.Error != "" || len(rec.IPs) != 2 {
		t.Fatalf("foreign lines are skipped, the rest kept: %+v", rec)
	}
	good = false
	p.fetchURL(context.Background(), srv.URL)
	rec = p.st.URLs[srv.URL]
	if rec.Error == "" || len(rec.IPs) != 2 || strings.Contains(rec.Error, srv.URL) {
		t.Fatalf("a failed fetch keeps the old list and reports without the address: %+v", rec)
	}
	if again := newPreferredPool(dir); len(again.st.URLs[srv.URL].IPs) != 2 {
		t.Fatal("cache must persist")
	}
	if _, err := p.download(context.Background(), "http://example.com/list"); err == nil {
		t.Fatal("plain http must be refused")
	}
}

func TestScanKeepsBestAndRemeasuresPool(t *testing.T) {
	p := newPreferredPool(t.TempDir())
	var tested []netip.Addr
	done := make(chan struct{})
	p.scanFn = func(_ context.Context, a []netip.Addr, host string) []cfpreferred.Result {
		defer close(done)
		tested = a
		if host != "win.example.com" {
			t.Error(host)
		}
		return []cfpreferred.Result{
			{Addr: a[0], Sent: 3, Latency: 50 * time.Millisecond},
			{Addr: a[1], Sent: 3, Lost: 1, Latency: 70 * time.Millisecond},
			{Addr: a[2], Sent: 3, Lost: 2, Latency: 90 * time.Millisecond},
			{Addr: netip.MustParseAddr("104.16.0.1"), Sent: 3, Latency: 120 * time.Millisecond},
		}
	}
	id := "dev-" + strings.Repeat("a", 64)
	cfg := config.Cloudflare{Peers: map[string]string{id: "win.example.com"}, PreferredIPs: []string{"104.16.0.1"},
		PreferredIP: &config.PreferredIP{Scan: &config.PreferredScan{Enabled: true, Sample: 10, Keep: 2}}}
	if err := p.startScan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for p.state(cfg).Scan.Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(tested) < 11 || tested[len(tested)-1].String() != "104.16.0.1" {
		t.Fatal("the pooled address must be re-measured too:", len(tested))
	}
	st := p.state(cfg)
	if st.Scan.Kept != 2 || st.Scan.Running || st.Scan.LastAt == nil || st.Scan.Error != "" || st.Scan.NextAt == nil {
		t.Fatalf("%+v", st.Scan)
	}
	if len(st.Pool) != 3 || st.Pool[0].IP != "104.16.0.1" || st.Pool[0].LatencyMS == nil || *st.Pool[0].LatencyMS != 120 {
		t.Fatalf("%+v", st.Pool)
	}
	if st.Pool[1].Source != "scan" || *st.Pool[1].LossPercent != 0 || *st.Pool[2].LossPercent != 33 {
		t.Fatalf("%+v", st.Pool)
	}
	if err := p.startScan(context.Background(), config.Cloudflare{}); err == nil {
		t.Fatal("a scan without a peer hostname must say why")
	}
}

func TestInspectClassifiesLines(t *testing.T) {
	p := newPreferredPool("")
	cfg := config.Cloudflare{PreferredIPs: []string{"104.16.0.1"}}
	r := p.inspect(cfg, "IP 地址,已发送\n104.16.0.1,4\n104.16.0.2,4\n104.16.0.2\n8.8.8.8\n\n# c\n")
	var got []string
	for _, it := range r.Items {
		got = append(got, it.IP+":"+it.Status)
	}
	if strings.Join(got, " ") != "104.16.0.1:existing 104.16.0.2:new 104.16.0.2:existing 8.8.8.8:rejected" {
		t.Fatal(got)
	}
	if len(r.Accepted) != 1 || r.Accepted[0] != "104.16.0.2" || r.Items[3].Line != 5 {
		t.Fatalf("%+v", r)
	}
}

func TestProviderIgnoresPreferredSettings(t *testing.T) {
	a := config.Mesh{Cloudflare: config.Cloudflare{Enabled: true, Hostname: "mac.example.com"}}
	b := a
	b.Cloudflare.PreferredIPs = []string{"104.16.0.1"}
	b.Cloudflare.PreferredIP = &config.PreferredIP{Scan: &config.PreferredScan{Enabled: true}}
	if !providerSame(a, b, device.Cloudflare) {
		t.Fatal("a preferred-address change must not rebuild the Cloudflare path")
	}
	b.Cloudflare.Hostname = "other.example.com"
	if providerSame(a, b, device.Cloudflare) {
		t.Fatal("a hostname change still rebuilds it")
	}
}

func TestLossCountsAgainstLowLatency(t *testing.T) {
	p := newPreferredPool("")
	p.st.Scan.Results = []scanRow{{IP: "104.16.0.1", LatencyMS: 40, Loss: 66}, {IP: "104.16.0.2", LatencyMS: 100, Loss: 0}}
	cfg := config.Cloudflare{PreferredIPs: []string{"104.16.0.1", "104.16.0.2"}}
	if got := addrs(t, p.dialOrder(cfg, "smart", nil)); got != "104.16.0.2,104.16.0.1" {
		t.Fatal(got)
	}
}

func TestSecondScanKeepsAndReplacesScanAddresses(t *testing.T) {
	p := newPreferredPool("")
	id := "dev-" + strings.Repeat("a", 64)
	cfg := config.Cloudflare{Peers: map[string]string{id: "win.example.com"}, PreferredIP: &config.PreferredIP{Scan: &config.PreferredScan{Enabled: true, Sample: 10, Keep: 2}}}
	p.st.Scan.Results = []scanRow{{IP: "104.16.0.1", LatencyMS: 50, Keep: true}, {IP: "104.16.0.2", LatencyMS: 60, Keep: true}}
	var tested []netip.Addr
	p.scanFn = func(_ context.Context, a []netip.Addr, _ string) []cfpreferred.Result {
		tested = a
		return []cfpreferred.Result{
			{Addr: netip.MustParseAddr("104.16.0.1"), Sent: 3, Latency: 30 * time.Millisecond},
			{Addr: netip.MustParseAddr("172.64.0.5"), Sent: 3, Latency: 40 * time.Millisecond},
			{Addr: netip.MustParseAddr("104.16.0.2"), Sent: 3, Latency: 90 * time.Millisecond},
		}
	}
	if err := p.startScan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400 && p.state(cfg).Scan.Running; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !slices.Contains(tested, netip.MustParseAddr("104.16.0.1")) || !slices.Contains(tested, netip.MustParseAddr("104.16.0.2")) {
		t.Fatal("kept scan addresses must be measured again")
	}
	var kept []string
	for _, m := range p.members(cfg) {
		kept = append(kept, m.addr.String())
	}
	if strings.Join(kept, ",") != "104.16.0.1,172.64.0.5" {
		t.Fatal("the two fastest compete for the kept slots:", kept)
	}
}

func TestInspectStopsAtPoolCapacity(t *testing.T) {
	var lines []string
	for i := range 70 {
		lines = append(lines, "104.16.1."+strconv.Itoa(i+1))
	}
	r := newPreferredPool("").inspect(config.Cloudflare{}, strings.Join(lines, "\n"))
	if len(r.Accepted) != cfpreferred.MaxList || r.Items[69].Status != "rejected" {
		t.Fatal(len(r.Accepted), r.Items[69])
	}
}

func scanResults() []cfpreferred.Result {
	r := func(ip, colo string, ms int) cfpreferred.Result {
		return cfpreferred.Result{Addr: netip.MustParseAddr(ip), Sent: 3, Latency: time.Duration(ms) * time.Millisecond, Colo: colo}
	}
	return []cfpreferred.Result{
		r("104.16.1.1", "HKG", 40), r("104.16.2.1", "NRT", 60), r("104.16.3.1", "LAX", 180),
		r("104.16.4.1", "FRA", 300), r("104.16.5.1", "", 310),
	}
}

func finish(p *preferredPool, scan config.PreferredScan) []string {
	p.scanning = true
	p.finishScan(context.Background(), "win.example.com", 5, scanResults(), scan.Effective(), nil)
	var kept []string
	for _, r := range p.st.Scan.Results {
		if r.Keep {
			kept = append(kept, r.Colo)
		}
	}
	return kept
}

func TestScanAutoKeepsFastestRegions(t *testing.T) {
	p := newPreferredPool("")
	got := finish(p, config.PreferredScan{Keep: 8})
	if strings.Join(got, " ") != "HKG NRT" || strings.Join(p.st.Scan.Chosen, " ") != "hk jp" {
		t.Fatalf("%v %v", got, p.st.Scan.Chosen)
	}
	if p.st.Scan.Unidentified != 1 || len(p.st.Scan.Regions) != 4 || p.st.Scan.Regions[0].Key != "hk" {
		t.Fatalf("%+v", p.st.Scan)
	}
	if p.st.Nets["104.16.1.0/24"] != "HKG" {
		t.Fatal(p.st.Nets)
	}
}

func TestScanPickAndNone(t *testing.T) {
	p := newPreferredPool("")
	if got := finish(p, config.PreferredScan{Keep: 8, RegionMode: "pick", Regions: []string{"eu", "lax"}}); strings.Join(got, " ") != "LAX FRA" {
		t.Fatal(got)
	}
	if got := finish(p, config.PreferredScan{Keep: 8, RegionMode: "none"}); len(got) != 5 {
		t.Fatal(got)
	}
	if p2 := newPreferredPool(""); true {
		finish(p2, config.PreferredScan{Keep: 8, RegionMode: "pick", Regions: []string{"hk", "kr"}})
		if strings.Join(p2.st.Scan.Chosen, " ") != "hk" {
			t.Fatal("chosen must only list what the scan found:", p2.st.Scan.Chosen)
		}
	}
	// Nothing matches: the previous results stay and an error says why.
	before := p.st.Scan.Results
	if got := finish(p, config.PreferredScan{Keep: 8, RegionMode: "pick", Regions: []string{"kr"}}); len(got) != 5 || p.st.Scan.Error == "" || len(p.st.Scan.Results) != len(before) {
		t.Fatalf("%v %+v", got, p.st.Scan)
	}
}

func TestScanWithoutAnyColoDoesNotFilter(t *testing.T) {
	p := newPreferredPool("")
	p.scanning = true
	res := scanResults()
	for i := range res {
		res[i].Colo = ""
	}
	p.finishScan(context.Background(), "h", 5, res, config.PreferredScan{Keep: 3}.Effective(), nil)
	n := 0
	for _, r := range p.st.Scan.Results {
		if r.Keep {
			n++
		}
	}
	if n != 3 || p.st.Scan.Error != "" {
		t.Fatalf("%d %+v", n, p.st.Scan)
	}
}

func TestPoolEntriesShowColo(t *testing.T) {
	p := newPreferredPool("")
	finish(p, config.PreferredScan{Keep: 8})
	cfg := config.Cloudflare{PreferredIPs: []string{"104.16.1.77"}, PreferredIP: &config.PreferredIP{Scan: &config.PreferredScan{Enabled: true}}}
	st := p.state(cfg)
	if st.Pool[0].Colo != "HKG" || st.Pool[0].Region != "hk" { // learned from its /24
		t.Fatalf("%+v", st.Pool[0])
	}
}

func TestPickRegionKeyIsNotAColoCode(t *testing.T) {
	// "sea" is Southeast Asia; SEA is Seattle.
	if regionWanted([]string{"sea"}, "SEA") || !regionWanted([]string{"sea"}, "BKK") {
		t.Fatal("a region key must not match a colo code")
	}
	if !regionWanted([]string{"SEA"}, "sea") || regionWanted([]string{"SEA"}, "BKK") {
		t.Fatal("an upper-case entry is a colo code")
	}
}

func TestSamplingSkipsCachedIPv6WhenDisabled(t *testing.T) {
	p := newPreferredPool("")
	p.st.Nets = map[string]string{"2606:4700:1::/48": "HKG"}
	p.st.Scan.Chosen = []string{"hk"}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, a := range p.sampleAddrs(config.PreferredScan{Sample: 40, RegionMode: "auto", RegionCount: 2}, rng) {
		if a.Is6() {
			t.Fatal(a)
		}
	}
}
