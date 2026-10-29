package webhookmod

// SetURLCheck replaces the public-address check and returns the undo.
func SetURLCheck(f func(string) error) func() {
	old := checkURL
	checkURL = f
	return func() { checkURL = old }
}
