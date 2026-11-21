package catalog

// SetAdditionsForTest replaces the bundled additions until the test ends, so
// the registry tests can check the hooks while additions/ is empty.
func SetAdditionsForTest(cleanup func(func()), models, codexClientModels []byte) {
	oldModels, oldClient := additionalModels, additionalCodexClientModels
	additionalModels, additionalCodexClientModels = models, codexClientModels
	cleanup(func() {
		additionalModels, additionalCodexClientModels = oldModels, oldClient
	})
}
