package urlcheck

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"unicode"
)

// errRejected is returned for every refused URL. The HTTP layer owns the
// text a person sees.
var errRejected = errors.New("not a public http or https URL")

// Validate checks raw as a public http or https URL.
// On success it returns raw with leading and trailing spaces removed.
// The path, query, and fragment are not rewritten. It does not dial the
// host and does not resolve DNS.
func Validate(raw string) (string, error) {
	trimmed := strings.Trim(raw, " ")
	if !publicHTTP(trimmed) {
		return "", errRejected
	}
	return trimmed, nil
}

func publicHTTP(raw string) bool {
	if len(raw) < 1 || len(raw) > 2048 {
		return false
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}

	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return false
	}

	// One trailing dot is the FQDN form of the same host. The returned URL
	// is still the trimmed input.
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || isLocalName(host) {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !blockedIP(ip)
	}
	return strings.Contains(host, ".")
}

func isLocalName(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local")
}

// blockedIP reports loopback, private (RFC 1918 and IPv6 unique-local),
// link-local, multicast, unspecified, and shared 100.64.0.0/10 addresses.
// ip.To4 covers IPv4-mapped IPv6 forms of those IPv4 ranges.
func blockedIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return true
	}
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}
