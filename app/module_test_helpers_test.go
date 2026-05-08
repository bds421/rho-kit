package app

// Test helpers for builder integration tests. The helpers used to live
// in database_module_test.go (deleted with the GORM removal). Lifting
// them into a dedicated test-only file keeps every other module's
// integration test self-contained.

func hasModule(modules []Module, name string) bool {
	for _, m := range modules {
		if m.Name() == name {
			return true
		}
	}
	return false
}

func moduleNames(modules []Module) []string {
	out := make([]string, len(modules))
	for i, m := range modules {
		out[i] = m.Name()
	}
	return out
}
