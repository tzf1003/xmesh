package connectivity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/device"
)

// receiveOffer stores a connection-method offer from a paired device. The listener's pairing gate has
// already required a trusted client certificate; the sender recorded with the offer is the one that
// certificate proves, never a name in the body. Nothing is applied until the user accepts it.
func (n *Node) receiveOffer(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
		http.NotFound(w, r)
		return
	}
	from, err := n.trust.VerifyCertificate(r.TLS.PeerCertificates[0], "")
	if err != nil || from.Revoked {
		http.Error(w, "设备未配对", http.StatusForbidden)
		return
	}
	var offer device.Offer
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, device.MaxOfferBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&offer); err != nil {
		http.Error(w, device.ErrOfferInvalid.Error(), http.StatusBadRequest)
		return
	}
	switch err := n.offers.Receive(r.Context(), from, offer); {
	case errors.Is(err, device.ErrOfferInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, device.ErrOfferFull):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
	case err != nil:
		http.Error(w, "无法保存连接方式提议", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// SendOffer delivers an offer to a paired device over any path currently verified for it. The device
// stores it for its user to accept; a delivery says nothing about whether it will be applied.
func (n *Node) SendOffer(ctx context.Context, id string, offer device.Offer) error {
	if err := offer.Validate(); err != nil {
		return err
	}
	if _, ok := n.trust.Find(id); !ok {
		return device.ErrUntrusted
	}
	body, err := json.Marshal(offer)
	if err != nil {
		return err
	}
	var last error = fmt.Errorf("设备 %s 当前没有可用的连接路径", id)
	devices, _ := n.directory.Snapshot(time.Now())
	for _, d := range devices {
		if d.DeviceID != id || d.IdentityConflict {
			continue
		}
		for _, path := range d.Paths {
			if !path.Reachable {
				continue
			}
			if last = n.postOffer(ctx, id, path, body); last == nil {
				return nil
			}
		}
	}
	return last
}

func (n *Node) postOffer(ctx context.Context, id string, path device.Path, body []byte) error {
	client, err := n.httpClient(path.Kind, id, false)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(path.Address, "/")+device.OfferPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	reason, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	return fmt.Errorf("对端拒绝了连接方式提议 (%d): %s", response.StatusCode, strings.TrimSpace(string(reason)))
}
