package dnsacct

// SetLookupNS replaces the public NS lookup and returns the undo.
func SetLookupNS(f func(zone string) []string) func() {
	old := lookupNS
	lookupNS = f
	return func() { lookupNS = old }
}

// Best exposes the account choice without any network.
func (a *Accounts) Best(name string) (Account, string) { return a.best(name) }
