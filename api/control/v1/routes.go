package v1

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/tzf1003/xmesh/docs/contracts/schemas"
)

// Route is one row of the route table (docs/contracts/schemas/control-routes.json). Query, Body
// and Response are "urn:xmesh:schema:control-<x>:1#/$defs/<Name>" references, or nil.
type Route struct {
	ID         string      `json:"id"`
	Method     string      `json:"method"`
	Path       string      `json:"path"`
	Capability Capability  `json:"capability"`
	Since      string      `json:"since"`
	Query      *string     `json:"query"`
	Body       *string     `json:"body"`
	Response   *string     `json:"response"`
	Success    int         `json:"success"`
	Stream     bool        `json:"stream"`
	IfMatch    string      `json:"if_match"`
	Errors     []ErrorCode `json:"errors"`
}

var (
	routesOnce sync.Once
	routes     []Route
	routesErr  error
)

// Routes returns the embedded route table. The kernel registers exactly these routes and the
// assertion lives in its tests.
func Routes() []Route {
	routesOnce.Do(func() {
		var file struct {
			APIVersion string  `json:"api_version"`
			Routes     []Route `json:"routes"`
		}
		if routesErr = json.Unmarshal(schemas.ControlRoutes, &file); routesErr == nil && file.APIVersion != APIVersion {
			routesErr = fmt.Errorf("control: route table is %s, package is %s", file.APIVersion, APIVersion)
		}
		routes = file.Routes
	})
	if routesErr != nil {
		panic(routesErr)
	}
	return append([]Route(nil), routes...)
}

// RouteByID returns the route with the given id.
func RouteByID(id string) (Route, bool) {
	for _, r := range Routes() {
		if r.ID == id {
			return r, true
		}
	}
	return Route{}, false
}
