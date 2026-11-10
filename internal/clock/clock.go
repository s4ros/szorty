package clock

import "time"

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// Real is the wall clock.
type Real struct{}

// Now returns the current UTC time.
func (Real) Now() time.Time {
	return time.Now().UTC()
}

var _ Clock = Real{}
