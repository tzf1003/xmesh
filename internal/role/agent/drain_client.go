package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/tzf1003/xmesh/internal/mesh"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// DrainRequest is the body of POST /control/drain (CTR-GW-05).
type DrainRequest struct {
	Scope  string `json:"scope"`
	Device string `json:"device,omitempty"`
	Drain  string `json:"drain,omitempty"`
}

// DrainOutcome is one device's answer to a drain request.
type DrainOutcome struct {
	Device string
	Err    error
}

func controlURL(peer mesh.Peer) (string, error) {
	u, e := url.Parse(peer.Address)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("设备地址无效")
	}
	u.Path, u.RawQuery, u.Fragment = "/control/drain", "", ""
	return u.String(), nil
}

// SendDrain asks one Agent to drain. The caller's network identity must carry
// tag:admin on that Agent; the request itself carries no credential.
func SendDrain(ctx context.Context, client *http.Client, peer mesh.Peer, request DrainRequest) error {
	target, e := controlURL(peer)
	if e != nil {
		return e
	}
	body, _ := json.Marshal(request)
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", "application/json")
	response, e := client.Do(r)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	text, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(text)))
	}
	return nil
}

// FanoutDrain sends the request to every peer concurrently and reports each
// outcome, ordered by device name. One unreachable device never stops the
// others: a mesh-wide drain is best effort per device and the caller decides
// how to treat failures.
func FanoutDrain(ctx context.Context, peers []mesh.Peer, client func(mesh.Peer) *http.Client, request DrainRequest, perDevice time.Duration) []DrainOutcome {
	out := make([]DrainOutcome, len(peers))
	limit := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, peer := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			callCtx, cancel := context.WithTimeout(ctx, perDevice)
			defer cancel()
			out[i] = DrainOutcome{Device: peer.Name, Err: SendDrain(callCtx, client(peer), peer, request)}
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}
