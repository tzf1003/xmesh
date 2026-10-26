package cfpreferred

import (
	"net/netip"
	"strings"
)

// RegionInfo names one group of Cloudflare data centres ("colos", identified by the IATA code of the nearest
// airport). Grouping them keeps "pick a region" short: there are over three hundred colos.
type RegionInfo struct {
	Key  string
	Name string
}

// RegionOther collects every colo the table below does not place.
const RegionOther = "other"

// Regions lists the groups in the order they are shown.
var Regions = []RegionInfo{
	{"hk", "港澳台"}, {"jp", "日本"}, {"kr", "韩国"}, {"sg", "新加坡"}, {"sea", "东南亚"},
	{"us-w", "美西"}, {"us-e", "美东"}, {"eu", "欧洲"}, {RegionOther, "其他地区"},
}

var coloRegion = func() map[string]string {
	m := map[string]string{}
	for key, codes := range map[string]string{
		"hk":   "HKG MFM TPE KHH",
		"jp":   "NRT KIX FUK OKA ITM CTS HND NGO",
		"kr":   "ICN PUS GMP",
		"sg":   "SIN",
		"sea":  "BKK KUL CGK MNL SGN HAN DAD HKT CEB DPS PNH RGN VTE JHB SUB MDC",
		"us-w": "LAX SJC SEA PDX SFO LAS PHX SLC DEN SAN SMF BOI ANC HNL TUS ABQ",
		"us-e": "EWR IAD ORD ATL MIA DFW IAH BOS MCI MSP DTW CLT PHL RDU JAX TPA CMH IND PIT STL BNA BUF CLE MEM OMA ORF RIC CVG MKE OKC SDF TLH BGR YYZ YUL YOW YWG YVR YYC YXE",
		"eu":   "FRA LHR AMS CDG MAD MXP ARN VIE WAW PRG BRU ZRH DUB LIS MAN OSL CPH HEL ATH BUD OTP SOF BCN BEG BTS BUH DUS EDI HAM KBP LUX MRS MUC RIX TLL VNO ZAG LCA SKP TSE",
	} {
		for _, c := range strings.Fields(codes) {
			m[c] = key
		}
	}
	return m
}()

// RegionOf is the group a colo code belongs to: the table's key, RegionOther for a code it does not list, and
// "" when the colo is unknown.
func RegionOf(colo string) string {
	colo = strings.ToUpper(strings.TrimSpace(colo))
	if colo == "" {
		return ""
	}
	if k, ok := coloRegion[colo]; ok {
		return k
	}
	return RegionOther
}

// RegionName is the display name of a group key, or the key itself.
func RegionName(key string) string {
	for _, r := range Regions {
		if r.Key == key {
			return r.Name
		}
	}
	return key
}

// ValidRegion reports whether s is a group key or a three-letter colo code.
func ValidRegion(s string) bool {
	for _, r := range Regions {
		if r.Key == s {
			return true
		}
	}
	return ValidColo(s)
}

// ValidColo reports whether s looks like a colo code: three ASCII letters.
func ValidColo(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

// NetKey names the network an address belongs to for remembering which colo it lands in: its /24 for IPv4,
// its /48 for IPv6. Anycast gives every address in such a block the same colo from one vantage point.
func NetKey(a netip.Addr) string {
	bits := 24
	if a.Is6() {
		bits = 48
	}
	p, err := a.Prefix(bits)
	if err != nil {
		return ""
	}
	return p.String()
}

// SampleNets draws up to n distinct addresses at random from the given network keys (as NetKey writes them).
// Keys that do not parse or lie outside Cloudflare's ranges are skipped.
func SampleNets(nets []string, n int, rng interface{ IntN(int) int }) []netip.Addr {
	var ps []netip.Prefix
	for _, s := range nets {
		if p, err := netip.ParsePrefix(s); err == nil {
			if _, err := Parse(p.Addr().String()); err == nil {
				ps = append(ps, p)
			}
		}
	}
	if len(ps) == 0 {
		return nil
	}
	seen := map[netip.Addr]bool{}
	var out []netip.Addr
	for tries := 0; len(out) < n && tries < n*8; tries++ {
		a := randomIn(ps[rng.IntN(len(ps))], rng)
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}
