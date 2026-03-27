// Package conv provides generic type conversion helpers.
package conv

// String type-asserts v to a string. Returns the empty string if v is not a string.
func String(v any) string {
	s, _ := v.(string)
	return s
}
