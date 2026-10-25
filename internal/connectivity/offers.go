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

// OfferDelivery says how SendOffer got an offer to a device.
type OfferDelivery string

const (
	// OfferDelivered means the device has the offer in its inbox.
	OfferDelivered OfferDelivery = "delivered"
	// OfferQueued means no path from here reaches the device, so the offer waits for the device to
	// collect it over its own connection to this one.
	OfferQueued OfferDelivery = "queued"
)

// refusedError is an answer from the device itself: it is reachable and said no, so queueing the offer
// for it to collect would only be refused again.
type refusedError struct{ error }

// SendOffer delivers an offer to a paired device over any path currently verified for it. When none
// is reachable from here, as when the device only dials out to this one, it queues the offer for the
// device to collect over that connection. Either way the device applies nothing until its user accepts.
func (n *Node) SendOffer(ctx context.Context, id string, offer device.Offer) (OfferDelivery, error) {
	if err := offer.Validate(); err != nil {
		return "", err
	}
	if _, ok := n.trust.Find(id); !ok {
		return "", device.ErrUntrusted
	}
	body, err := json.Marshal(offer)
	if err != nil {
		return "", err
	}
	var last error = fmt.Errorf("设备 %s 当前没有可从本机拨通的连接路径", id)
	devices, _ := n.directory.Snapshot(time.Now())
	for _, d := range devices {
		if d.DeviceID != id || d.IdentityConflict {
			continue
		}
		for _, path := range d.Paths {
			if !path.Reachable {
				continue
			}
			if last = n.postOffer(ctx, id, path, device.OfferPath, body); last == nil {
				return OfferDelivered, nil
			}
			var refused refusedError
			if errors.As(last, &refused) {
				return "", last
			}
		}
	}
	if err := n.outbox.Queue(ctx, id, offer); err != nil {
		return "", fmt.Errorf("%w；也无法留着等对方来取：%v", last, err)
	}
	return OfferQueued, nil
}

type collectResponse struct {
	Offers []device.Offer `json:"offers"`
}

type ackRequest struct {
	IDs []string `json:"ids"`
}

// trustedCaller returns the paired, unrevoked device a request's client certificate proves.
func (n *Node) trustedCaller(w http.ResponseWriter, r *http.Request) (device.Trust, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
		http.NotFound(w, r)
		return device.Trust{}, false
	}
	from, err := n.trust.VerifyCertificate(r.TLS.PeerCertificates[0], "")
	if err != nil || from.Revoked {
		http.Error(w, "设备未配对", http.StatusForbidden)
		return device.Trust{}, false
	}
	return from, true
}

// collectOffersHandler gives a paired device the offers waiting for it. They stay queued until it
// acknowledges them, so a failed transfer is retried instead of lost.
func (n *Node) collectOffersHandler(w http.ResponseWriter, r *http.Request) {
	from, ok := n.trustedCaller(w, r)
	if !ok {
		return
	}
	pending, err := n.outbox.Pending(r.Context(), from.Document.DeviceID)
	if err != nil {
		http.Error(w, "无法读取待送达的连接方式", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(collectResponse{Offers: pending})
}

func (n *Node) ackOffersHandler(w http.ResponseWriter, r *http.Request) {
	from, ok := n.trustedCaller(w, r)
	if !ok {
		return
	}
	var req ackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || len(req.IDs) > device.MaxOutbox {
		http.Error(w, device.ErrOfferInvalid.Error(), http.StatusBadRequest)
		return
	}
	if err := n.outbox.Ack(r.Context(), from.Document.DeviceID, req.IDs); err != nil {
		http.Error(w, "无法确认连接方式", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// offerCollectEvery is how often a node asks the devices it can dial for offers queued for it.
var offerCollectEvery = 45 * time.Second

// collectLoop fetches offers other devices could not push to this one.
func (n *Node) collectLoop() {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-timer.C:
			n.collectOffers(n.ctx)
			timer.Reset(offerCollectEvery)
		}
	}
}

// collectOffers asks each reachable paired device for offers queued for this one, stores them in the
// inbox and then acknowledges them. Failures are retried on the next round.
func (n *Node) collectOffers(ctx context.Context) {
	devices, _ := n.directory.Snapshot(time.Now())
	for _, d := range devices {
		trusted, ok := n.trust.Find(d.DeviceID)
		if !ok || trusted.Revoked || d.IdentityConflict {
			continue
		}
		for _, path := range d.Paths {
			if !path.Reachable {
				continue
			}
			if n.collectFrom(ctx, trusted, path) {
				break
			}
		}
	}
}

func (n *Node) collectFrom(ctx context.Context, from device.Trust, path device.Path) bool {
	id := from.Document.DeviceID
	reply, err := n.postJSON(ctx, id, path, device.OfferPath+"/collect", struct{}{}, 1<<20)
	if err != nil {
		return false
	}
	var got collectResponse
	if json.Unmarshal(reply, &got) != nil || len(got.Offers) > device.MaxOutbox {
		return true
	}
	stored := []string{}
	for _, offer := range got.Offers {
		// An offer the inbox refuses is not acknowledged: the sender keeps it until it expires.
		if n.offers.Receive(ctx, from, offer) == nil {
			stored = append(stored, offer.ID)
		}
	}
	if len(stored) > 0 {
		_, _ = n.postJSON(ctx, id, path, device.OfferPath+"/ack", ackRequest{IDs: stored}, 0)
	}
	return true
}

func (n *Node) postJSON(ctx context.Context, id string, path device.Path, route string, body any, limit int64) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var reply []byte
	err = n.post(ctx, id, path, route, raw, func(r io.Reader) error {
		if limit == 0 {
			return nil
		}
		var readErr error
		reply, readErr = io.ReadAll(io.LimitReader(r, limit))
		return readErr
	})
	return reply, err
}

func (n *Node) postOffer(ctx context.Context, id string, path device.Path, route string, body []byte) error {
	return n.post(ctx, id, path, route, body, nil)
}

// post sends body to a paired device's route over path. A status the device itself answered with is a
// refusedError; anything else is a path failure.
func (n *Node) post(ctx context.Context, id string, path device.Path, route string, body []byte, read func(io.Reader) error) error {
	client, err := n.httpClient(path.Kind, id, false)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(path.Address, "/")+route, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent || (response.StatusCode == http.StatusOK && read != nil) {
		if read != nil {
			return read(response.Body)
		}
		return nil
	}
	reason, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	return refusedError{fmt.Errorf("对端拒绝了请求 (%d): %s", response.StatusCode, strings.TrimSpace(string(reason)))}
}
