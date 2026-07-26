// Package ptr holds the defensive-copy helpers that every package returning
// borrowed pointers needs. They existed in three copies before this package.
package ptr

// Clone returns a pointer to a copy of the pointed-to value, or nil for nil.
// Callers use it so a returned pointer never aliases cached or stored state.
func Clone[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
