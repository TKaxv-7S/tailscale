// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package appctest

import (
	"net/netip"
	"slices"

	"tailscale.com/appc/routeinfo"
	"tailscale.com/ipn"
)

// RouteCollector is a test helper that collects the list of routes advertised
type RouteCollector struct {
	routes        []netip.Prefix
	removedRoutes []netip.Prefix
	routeInfo     *routeinfo.RouteInfo
}

func (rc *RouteCollector) AdvertiseRoute(pfx ...netip.Prefix) error {
	rc.routes = append(rc.routes, pfx...)
	return nil
}

func (rc *RouteCollector) UnadvertiseRoute(toRemove ...netip.Prefix) error {
	routes := rc.routes
	rc.routes = rc.routes[:0]
	for _, r := range routes {
		if !slices.Contains(toRemove, r) {
			rc.routes = append(rc.routes, r)
		} else {
			rc.removedRoutes = append(rc.removedRoutes, r)
		}
	}
	return nil
}

func (rc *RouteCollector) StoreRouteInfo(ri *routeinfo.RouteInfo) error {
	rc.routeInfo = ri
	return nil
}

func (rc *RouteCollector) ReadRouteInfo() (*routeinfo.RouteInfo, error) {
	if rc.routeInfo == nil {
		return nil, ipn.ErrStateNotExist
	}
	return rc.routeInfo, nil
}

// RemovedRoutes returns the list of routes that were removed.
func (rc *RouteCollector) RemovedRoutes() []netip.Prefix {
	return rc.removedRoutes
}

// Routes returns the ordered list of routes that were added, including
// possible duplicates.
func (rc *RouteCollector) Routes() []netip.Prefix {
	return rc.routes
}

func (rc *RouteCollector) SetRoutes(routes []netip.Prefix) error {
	rc.routes = routes
	return nil
}
