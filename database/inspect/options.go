package sqlinspect

// NormalizeDriver resolves supported driver aliases to their catalog backend.
func NormalizeDriver(driver string) string { return normalizeDriver(driver) }

// WithDefaults resolves zero catalog limits to the documented bounds.
func (l Limits) WithDefaults() Limits { return l.withDefaults() }

// ValidateDriver rejects backends without catalog queries.
func ValidateDriver(driver string) error {
	_, _, err := inspectionQueries(driver)
	return err
}
