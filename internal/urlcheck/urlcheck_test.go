package urlcheck

import (
	"strings"
	"testing"
)

func TestValidateAccepts(t *testing.T) {
	const example = "https://example.com/a?b=1#f"
	base := "https://example.com/"
	exact := base + strings.Repeat("a", 2048-len(base))
	if len(exact) != 2048 {
		t.Fatalf("fixture length = %d, want 2048", len(exact))
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "query and fragment", raw: example, want: example},
		{name: "trimmed spaces", raw: "  " + example + "  ", want: example},
		{name: "public ipv4", raw: "https://1.1.1.1/", want: "https://1.1.1.1/"},
		{name: "public ipv4 http", raw: "http://8.8.8.8/path?q=1#x", want: "http://8.8.8.8/path?q=1#x"},
		{name: "public ipv4 port", raw: "https://1.1.1.1:443/", want: "https://1.1.1.1:443/"},
		{name: "http host", raw: "http://example.com/path", want: "http://example.com/path"},
		{name: "uppercase scheme kept", raw: "HTTPS://example.com/a", want: "HTTPS://example.com/a"},
		{name: "public ipv6", raw: "https://[2606:4700:4700::1111]/", want: "https://[2606:4700:4700::1111]/"},
		{name: "mapped public ipv4", raw: "https://[::ffff:1.1.1.1]/", want: "https://[::ffff:1.1.1.1]/"},
		{name: "port query fragment", raw: "https://example.com:8443/a?b=1#f", want: "https://example.com:8443/a?b=1#f"},
		{name: "path not normalized", raw: "https://example.com/a/../b", want: "https://example.com/a/../b"},
		{name: "encoded space kept", raw: "https://example.com/a?b=1%20c#f", want: "https://example.com/a?b=1%20c#f"},
		{name: "trailing dot on name", raw: "https://example.com./a", want: "https://example.com./a"},
		{name: "below 172.16/12", raw: "https://172.15.255.255/", want: "https://172.15.255.255/"},
		{name: "above 172.16/12", raw: "https://172.32.0.1/", want: "https://172.32.0.1/"},
		{name: "below 192.168/16", raw: "https://192.167.255.255/", want: "https://192.167.255.255/"},
		{name: "above 192.168/16", raw: "https://192.169.0.1/", want: "https://192.169.0.1/"},
		{name: "below 10/8", raw: "https://9.255.255.255/", want: "https://9.255.255.255/"},
		{name: "above 10/8", raw: "https://11.0.0.1/", want: "https://11.0.0.1/"},
		{name: "below 100.64/10", raw: "https://100.63.255.255/", want: "https://100.63.255.255/"},
		{name: "above 100.64/10", raw: "https://100.128.0.1/", want: "https://100.128.0.1/"},
		{name: "below link-local", raw: "https://169.253.255.255/", want: "https://169.253.255.255/"},
		{name: "above link-local", raw: "https://169.255.0.1/", want: "https://169.255.0.1/"},
		{name: "below multicast", raw: "https://223.255.255.255/", want: "https://223.255.255.255/"},
		{name: "localhost is only a label", raw: "https://localhost.example.com/", want: "https://localhost.example.com/"},
		{name: "local is only a label", raw: "https://evil.local.com/", want: "https://evil.local.com/"},
		{name: "name is not resolved", raw: "https://not-a-real-host.invalid/a", want: "https://not-a-real-host.invalid/a"},
		{name: "short public ipv4", raw: "https://8.8.8/", want: "https://8.8.8/"},
		{name: "max length", raw: exact, want: exact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Validate(tt.raw)
			if err != nil {
				t.Fatalf("Validate(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("Validate(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	base := "https://example.com/"
	tooLong := base + strings.Repeat("a", 2049-len(base))
	if len(tooLong) != 2049 {
		t.Fatalf("fixture length = %d, want 2049", len(tooLong))
	}

	tests := []struct {
		name string
		raw  string
	}{
		{name: "javascript", raw: "javascript:"},
		{name: "javascript payload", raw: "javascript:alert(1)"},
		{name: "data", raw: "data:"},
		{name: "data payload", raw: "data:text/plain,hello"},
		{name: "file", raw: "file:"},
		{name: "file path", raw: "file:///tmp/x"},
		{name: "scheme-less host", raw: "example.com"},
		{name: "scheme-less path", raw: "example.com/a"},
		{name: "scheme-relative", raw: "//example.com/a"},
		{name: "userinfo", raw: "https://user:pass@example.com/a"},
		{name: "userinfo user only", raw: "https://user@example.com/a"},
		{name: "userinfo on public ip", raw: "https://user:pass@1.1.1.1/"},
		{name: "localhost", raw: "https://localhost/"},
		{name: "localhost http", raw: "http://localhost/a"},
		{name: "localhost mixed case", raw: "http://LocalHost/a"},
		{name: "localhost upper", raw: "https://LOCALHOST/"},
		{name: "localhost port", raw: "https://localhost:8080/"},
		{name: "localhost trailing dot", raw: "https://localhost./"},
		{name: "dot localhost", raw: "https://foo.localhost/"},
		{name: "nested dot localhost", raw: "https://a.b.localhost/x"},
		{name: "dot localhost mixed case", raw: "https://Foo.LocalHost/"},
		{name: "dot localhost trailing dot", raw: "https://foo.localhost./"},
		{name: "dot local", raw: "https://printer.local/"},
		{name: "dot local mixed case", raw: "https://Foo.LOCAL/a"},
		{name: "dot local trailing dot", raw: "https://printer.local./"},
		{name: "loopback", raw: "http://127.0.0.1/"},
		{name: "short loopback", raw: "https://127.1/"},
		{name: "short loopback three", raw: "https://127.0.1/"},
		{name: "short private 10", raw: "https://10.1/"},
		{name: "short private 192.168", raw: "https://192.168.1/"},
		{name: "short private 172.16", raw: "https://172.16.1/"},
		{name: "short link local", raw: "https://169.254.43518/"},
		{name: "short cgnat", raw: "https://100.64.1/"},
		{name: "loopback port", raw: "https://127.0.0.1:80/"},
		{name: "loopback rest of range", raw: "https://127.255.255.255/"},
		{name: "loopback trailing dot", raw: "https://127.0.0.1./"},
		{name: "private 10", raw: "https://10.1.2.3/"},
		{name: "private 10 edge", raw: "https://10.255.255.255/"},
		{name: "private 192.168", raw: "https://192.168.0.1/"},
		{name: "private 192.168 edge", raw: "https://192.168.255.255/"},
		{name: "private 172.16", raw: "https://172.16.0.1/"},
		{name: "private 172.31", raw: "https://172.31.255.255/"},
		{name: "link local", raw: "https://169.254.169.254/"},
		{name: "link local edge", raw: "https://169.254.0.0/"},
		{name: "link local ipv6", raw: "https://[fe80::1]/"},
		{name: "link local ipv6 high", raw: "https://[febf::1]/"},
		{name: "ipv6 loopback", raw: "https://[::1]/"},
		{name: "ipv6 loopback port", raw: "https://[::1]:8080/"},
		{name: "cgnat", raw: "https://100.64.0.1/"},
		{name: "cgnat start", raw: "https://100.64.0.0/"},
		{name: "cgnat end", raw: "https://100.127.255.255/"},
		{name: "single label", raw: "https://printer"},
		{name: "single label path", raw: "https://intranet/a"},
		{name: "too long", raw: tooLong},
		{name: "interior space", raw: "https://example.com/a b"},
		{name: "space in host", raw: "https://ex ample.com/"},
		{name: "nbsp", raw: "https://example.com/a\u00a0b"},
		{name: "unspecified v4", raw: "https://0.0.0.0/"},
		{name: "unspecified v6", raw: "https://[::]/"},
		{name: "multicast v4", raw: "https://224.0.0.1/"},
		{name: "multicast v4 high", raw: "https://239.255.255.255/"},
		{name: "multicast v6", raw: "https://[ff02::1]/"},
		{name: "multicast v6 all", raw: "https://[ff00::1]/"},
		{name: "ula", raw: "https://[fc00::1]/"},
		{name: "ula fd", raw: "https://[fd12:3456:789a::1]/"},
		{name: "mapped loopback", raw: "https://[::ffff:127.0.0.1]/"},
		{name: "mapped loopback upper", raw: "https://[::FFFF:127.0.0.1]/"},
		{name: "mapped loopback hex", raw: "https://[::ffff:7f00:1]/"},
		{name: "mapped private 10", raw: "https://[::ffff:10.1.2.3]/"},
		{name: "mapped private 192.168", raw: "https://[::ffff:192.168.0.1]/"},
		{name: "mapped private 172.16", raw: "https://[::ffff:172.16.0.1]/"},
		{name: "mapped link local", raw: "https://[::ffff:169.254.169.254]/"},
		{name: "mapped cgnat", raw: "https://[::ffff:100.64.0.1]/"},
		{name: "mapped unspecified", raw: "https://[::ffff:0.0.0.0]/"},
		{name: "mapped multicast", raw: "https://[::ffff:224.0.0.1]/"},
		{name: "empty", raw: ""},
		{name: "spaces only", raw: "   "},
		{name: "leading tab", raw: "\thttps://example.com/"},
		{name: "interior nul", raw: "https://example.com/\x00"},
		{name: "empty host", raw: "https://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Validate(tt.raw)
			if err == nil {
				t.Fatalf("Validate(%q) = %q, want error", tt.raw, got)
			}
			if got != "" {
				t.Fatalf("Validate(%q) = %q, want empty string", tt.raw, got)
			}
		})
	}
}
