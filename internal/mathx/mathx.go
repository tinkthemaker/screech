// Package mathx holds the tiny numeric helpers screech needs in more than one
// package. Keeping them here stops each package from re-rolling its own copy.
package mathx

import "cmp"

// Clamp constrains x to the inclusive range [lo, hi].
func Clamp[T cmp.Ordered](x, lo, hi T) T {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
