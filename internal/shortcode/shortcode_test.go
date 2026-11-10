package shortcode

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func TestValid(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{name: "good code", code: "9AZa0z0", want: true},
		{name: "digits", code: "0123456", want: true},
		{name: "upper", code: "ABCDEFG", want: true},
		{name: "lower", code: "abcdefg", want: true},
		{name: "6 chars", code: "9AZa0z", want: false},
		{name: "8 chars", code: "9AZa0z0X", want: false},
		{name: "symbol", code: "9AZa0-0", want: false},
		{name: "empty", code: "", want: false},
		{name: "space", code: "abc def", want: false},
		{name: "underscore", code: "abc_def", want: false},
		{name: "slash", code: "abc/def", want: false},
		{name: "dot", code: "abc.def", want: false},
		{name: "before zero", code: "/123456", want: false},
		{name: "after nine", code: "012345:", want: false},
		{name: "before upper", code: "@BCDEFG", want: false},
		{name: "after upper", code: "ABCDE[G", want: false},
		{name: "before lower", code: "`bcdefg", want: false},
		{name: "after lower", code: "abcde{g", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Valid(tt.code)
			if got != tt.want {
				t.Fatalf("Valid(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestNewFixedReader(t *testing.T) {
	// Index is b%62 for b < 248. 248 and above are the 256%62 remainder;
	// a plain modulo would turn them into symbols 0-7 and bias those.
	raw := []byte{
		9, 10, 35, 36, 0, 61, 62,
		0, 248, 61, 255, 62, 186, 249, 247, 10, 36,
	}
	r := bytes.NewReader(raw)

	first, err := New(r)
	if err != nil {
		t.Fatalf("first New error = %v", err)
	}
	if first != "9AZa0z0" {
		t.Fatalf("first New = %q, want 9AZa0z0", first)
	}
	assertCode(t, first)

	second, err := New(r)
	if err != nil {
		t.Fatalf("second New error = %v", err)
	}
	if second != "0z00zAa" {
		t.Fatalf("second New = %q, want 0z00zAa", second)
	}
	assertCode(t, second)

	if r.Len() != 0 {
		t.Fatalf("reader has %d bytes left", r.Len())
	}
}

func TestNewShortRead(t *testing.T) {
	got, err := New(bytes.NewReader([]byte{0, 1, 2, 3, 4, 5}))
	if err == nil {
		t.Fatalf("New short read = %q, want error", got)
	}
	if got != "" {
		t.Fatalf("New short read = %q, want empty string", got)
	}
}

func TestNewRejectedTail(t *testing.T) {
	// Every byte is in the discarded remainder. None of them is a symbol.
	got, err := New(bytes.NewReader([]byte{248, 249, 250, 251, 252, 253, 254, 255}))
	if err == nil {
		t.Fatalf("New rejected tail = %q, want error", got)
	}
	if got != "" {
		t.Fatalf("New rejected tail = %q, want empty string", got)
	}
}

func TestNewReaderError(t *testing.T) {
	want := errors.New("rand failed")
	got, err := New(failReader{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("New error = %v, want %v", err, want)
	}
	if got != "" {
		t.Fatalf("New = %q, want empty string", got)
	}
}

func TestNewCryptoRand(t *testing.T) {
	for i := 0; i < 32; i++ {
		code, err := New(rand.Reader)
		if err != nil {
			t.Fatalf("New(crypto/rand.Reader) error = %v", err)
		}
		assertCode(t, code)
	}
}

func assertCode(t *testing.T, code string) {
	t.Helper()
	if len(code) != Length {
		t.Fatalf("len(%q) = %d, want %d", code, len(code), Length)
	}
	if !Valid(code) {
		t.Fatalf("Valid(%q) = false", code)
	}
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	for i := 0; i < len(code); i++ {
		if bytes.IndexByte([]byte(alphabet), code[i]) < 0 {
			t.Fatalf("%q contains byte %q outside the alphabet", code, code[i])
		}
	}
}

type failReader struct {
	err error
}

func (f failReader) Read([]byte) (int, error) {
	return 0, f.err
}

var _ io.Reader = failReader{}
