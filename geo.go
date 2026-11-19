package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strings"

	maxminddb "github.com/oschwald/maxminddb-golang/v2"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/text/language"
)

type geoLookup struct {
	db      *maxminddb.Reader
	trusted []netip.Prefix
}

func openGeo(path, proxies string) (*geoLookup, error) {
	geo := &geoLookup{}
	for value := range strings.SplitSeq(proxies, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid PORTAL_TRUSTED_PROXY CIDR %q: %w", value, err)
		}
		geo.trusted = append(geo.trusted, prefix.Masked())
	}
	defaultPath := path == ""
	if defaultPath {
		path = "geo/country.mmdb"
	}
	reader, err := maxminddb.Open(path)
	if err != nil && !(defaultPath && errors.Is(err, os.ErrNotExist)) {
		return nil, fmt.Errorf("open IP country database: %w", err)
	}
	geo.db = reader
	return geo, nil
}

func publicIP(ip netip.Addr) bool {
	return ip.Zone() == "" && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

func (g *geoLookup) applyTrustedProxy(r *http.Request) {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return
	}
	for _, prefix := range g.trusted {
		if prefix.Contains(peer.Addr().Unmap()) {
			forwarded, err := netip.ParseAddr(r.Header.Get("X-Real-IP"))
			if err == nil && forwarded.Zone() == "" {
				r.RemoteAddr = netip.AddrPortFrom(forwarded.Unmap(), 0).String()
			}
			return
		}
	}
}

func publicClientIP(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip := peer.Addr().Unmap()
	if !publicIP(ip) {
		return netip.Addr{}
	}
	return ip
}

func (g *geoLookup) country(ip netip.Addr) string {
	if g.db == nil || !ip.IsValid() {
		return ""
	}
	var country string
	if err := g.db.Lookup(ip).DecodePath(&country, "country", "iso_code"); err != nil || !validRegion(country) {
		return ""
	}
	return country
}

func (g *geoLookup) context(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, map[string]string{"country": g.country(publicClientIP(e.Request))})
}

func (g *geoLookup) network(e *core.RequestEvent) error {
	if e.Auth.GetString("role") != "member" {
		return e.ForbiddenError("", nil)
	}
	ip := publicClientIP(e.Request)
	if !ip.IsValid() {
		return e.JSON(http.StatusOK, map[string]string{"ip": "", "country": ""})
	}
	return e.JSON(http.StatusOK, map[string]string{"ip": ip.String(), "country": g.country(ip)})
}

func validRegion(code string) bool {
	if len(code) != 2 || code == "ZZ" {
		return false
	}
	region, err := language.ParseRegion(code)
	return err == nil && region.IsCountry()
}
