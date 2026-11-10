package shortcode

import "io"

const (
	// Length is the number of characters in a short code.
	Length = 7

	// alphabet is 0-9A-Za-z. Case is significant.
	alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	// 256 is not a multiple of 62. Bytes at or above acceptBelow are discarded
	// so each symbol is drawn from exactly four remaining values. Modulo over
	// that range does not bias the result.
	acceptBelow = 256 - (256 % len(alphabet))
)

// Valid reports whether code is a full match of ^[0-9A-Za-z]{7}$.
func Valid(code string) bool {
	if len(code) != Length {
		return false
	}
	for i := 0; i < Length; i++ {
		c := code[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		default:
			return false
		}
	}
	return true
}

// New reads Length characters from r. Each character is chosen uniformly
// from the 62-character alphabet. Production callers pass crypto/rand.Reader.
func New(r io.Reader) (string, error) {
	out := make([]byte, Length)
	var buf [1]byte
	for i := 0; i < Length; {
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return "", err
		}
		if int(buf[0]) >= acceptBelow {
			continue
		}
		out[i] = alphabet[int(buf[0])%len(alphabet)]
		i++
	}
	return string(out), nil
}
