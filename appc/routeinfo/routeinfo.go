// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package routeinfo

import (
	"net/netip"
	"time"
)

type RouteInfo struct {
	// routes set with --advertise-routes
	Local []netip.Prefix
	// routes from the 'routes' section of an app connector acl
	Control []netip.Prefix
	// routes discovered by observing dns lookups for configured domains
	Discovered map[string]*DatedRoutes
}

func NewRouteInfo() *RouteInfo {
	discovered := make(map[string]*DatedRoutes)
	return &RouteInfo{
		Local:      []netip.Prefix{},
		Control:    []netip.Prefix{},
		Discovered: discovered,
	}
}

// RouteInfo.Routes returns a slice containing all the routes stored from the wanted resources.
func (ri *RouteInfo) Routes(local, control, discovered bool) []netip.Prefix {
	var ret []netip.Prefix
	if local {
		ret = ri.Local
	}
	if control && len(ret) == 0 {
		ret = ri.Control
	} else if control {
		ret = append(ret, ri.Control...)
	}

	if discovered {
		for _, dr := range ri.Discovered {
			ret = append(ret, dr.RoutesSlice()...)
		}
	}
	return ret
}

type DatedRoutes struct {
	// routes discovered for a domain, and when they were last seen in a dns query
	Routes map[netip.Prefix]time.Time
	// the time at which we last expired old routes
	LastCleanup time.Time
}

func (dr *DatedRoutes) RoutesSlice() []netip.Prefix {
	var routes []netip.Prefix
	for k := range dr.Routes {
		routes = append(routes, k)
	}
	return routes
}

func (r *RouteInfo) AddRoutesInDiscoveredForDomain(domain string, addrs []netip.Prefix) {
	dr, hasKey := r.Discovered[domain]
	if !hasKey || dr == nil || dr.Routes == nil {
		newDatedRoutes := &DatedRoutes{make(map[netip.Prefix]time.Time), time.Now()}
		newDatedRoutes.addAddrsToDatedRoute(addrs)
		r.Discovered[domain] = newDatedRoutes
		return
	}

	// kevin comment: we won't see any existing routes here because know addrs are filtered.
	currentRoutes := r.Discovered[domain]
	currentRoutes.addAddrsToDatedRoute(addrs)
	r.Discovered[domain] = currentRoutes
	return
}

func (d *DatedRoutes) addAddrsToDatedRoute(addrs []netip.Prefix) {
	time := time.Now()
	for _, addr := range addrs {
		d.Routes[addr] = time
	}
}