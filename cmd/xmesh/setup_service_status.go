package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tzf1003/xmesh/internal/control"
	"io"
	"strings"
	"time"
)

// A rollback may restore a pre-upgrade kernel. Read only its advertised status
// endpoint; configuration operations continue to require the current API major.
func setupServiceResponding(ctx context.Context, ep control.Endpoint) error {
	major := strings.Split(ep.APIVersion, ".")[0]
	if major != "1" && major != "2" {
		return errors.New("unsupported saved control API")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := control.NewClient(ep).Stream(ctx, "/v"+major+"/status", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || strings.Split(res.Header.Get("X-Xmesh-Control-Version"), ".")[0] != major {
		return errors.New("saved service status unavailable")
	}
	var status struct {
		Hostname string `json:"hostname"`
		PID      int    `json:"pid"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&status); err != nil {
		return err
	}
	if status.Hostname != ep.Node || status.PID != ep.PID {
		return fmt.Errorf("saved service identity differs")
	}
	return nil
}
