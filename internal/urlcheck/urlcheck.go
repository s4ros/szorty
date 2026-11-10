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
	// Browsers expand 1–4 part decimal IPv4 (no leading zeros). Judge the
	// expanded address; the returned URL stays as written.
	if ip, ok := decimalIPv4(host); ok {
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

// decimalIPv4 parses a 1–4 part decimal IPv4 literal with no leading zeros.
// The last part must be smaller than 256^(5−parts), as in the URL Standard.
// It returns false for any other host, including IPv4-compatible IPv6 text.
func decimalIPv4(host string) (net.IP, bool) {
	parts := strings.Split(host, ".")
	nparts := len(parts)
	if nparts < 1 || nparts > 4 {
		return nil, false
	}
	nums := make([]uint64, nparts)
	for i, part := range parts {
		n, ok := decimalPart(part)
		if !ok {
			return nil, false
		}
		nums[i] = n
	}
	for i := 0; i < nparts-1; i++ {
		if nums[i] > 255 {
			return nil, false
		}
	}
	limit := uint64(1)
	for i := 0; i < 5-nparts; i++ {
		limit *= 256
	}
	if nums[nparts-1] >= limit {
		return nil, false
	}
	ipv4 := nums[nparts-1]
	for i := 0; i < nparts-1; i++ {
		pow := uint64(1)
		for k := 0; k < 3-i; k++ {
			pow *= 256
		}
		ipv4 += nums[i] * pow
	}
	return net.IPv4(byte(ipv4>>24), byte(ipv4>>16), byte(ipv4>>8), byte(ipv4)), true
}

func decimalPart(part string) (uint64, bool) {
	if part == "" || (len(part) > 1 && part[0] == '0') {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(part); i++ {
		c := part[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		digit := uint64(c - '0')
		if n > (0xffffffff-digit)/10 {
			return 0, false
		}
		n = n*10 + digit
	}
	return n, true
}
