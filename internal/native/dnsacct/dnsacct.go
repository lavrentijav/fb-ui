// Package dnsacct keeps the fleet's DNS accounts in the masters' state and
// picks the account and zone that hold a name.
package dnsacct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/native/master"
)

const Table = "dnsacct"

var ErrNoZone = errors.New("dnsacct: no DNS account holds this name")

// Reader is the projected state.
type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
	Keys(table string) []string
}

// Account is one provider login; Zones are discovered, not typed.
type Account struct {
	Type       string   `json:"type"`
	BaseURL    string   `json:"baseUrl,omitempty"`
	Credential string   `json:"credential,omitempty"`
	Zones      []string `json:"zones"`
}

// Accounts reads and writes accounts; Discover lists an account's zones
// (dnsprovider.ListZones outside tests).
type Accounts struct {
	Master   *master.Master
	State    Reader
	Discover func(ctx context.Context, cfg dnsprovider.Config) ([]string, error)
	// ResyncEvery limits rediscovery on a miss (default 1 min).
	ResyncEvery time.Duration

	mu         sync.Mutex
	lastResync time.Time
}

func (a *Accounts) discover(ctx context.Context, acc Account) ([]string, error) {
	cfg := dnsprovider.Config{Type: acc.Type, Credential: acc.Credential, BaseURL: acc.BaseURL}
	if a.Discover != nil {
		return a.Discover(ctx, cfg)
	}
	return dnsprovider.ListZones(ctx, cfg)
}

// Put saves an account once its credential lists its zones, so a wrong
// token is refused here and not at the next renewal.
func (a *Accounts) Put(ctx context.Context, id string, acc Account) (Account, error) {
	if id == "" {
		return Account{}, errors.New("dnsacct: id is required")
	}
	zones, err := a.discover(ctx, acc)
	if err != nil {
		return Account{}, err
	}
	acc.Zones = zones
	_, err = a.Master.Set(ctx, Table, id, map[string]any{
		"type": acc.Type, "baseUrl": acc.BaseURL, "credential": acc.Credential, "zones": zones,
	})
	return acc, err
}

func (a *Accounts) Delete(ctx context.Context, id string) error {
	_, err := a.Master.Delete(ctx, Table, id)
	return err
}

func (a *Accounts) get(id string) (Account, bool) {
	rec, ok := a.State.Get(Table, id)
	if !ok {
		return Account{}, false
	}
	raw, _ := json.Marshal(rec)
	var acc Account
	return acc, json.Unmarshal(raw, &acc) == nil
}

// List is what the panel shows: no credentials.
func (a *Accounts) List() map[string]Account {
	out := map[string]Account{}
	for _, id := range a.State.Keys(Table) {
		if acc, ok := a.get(id); ok {
			acc.Credential = ""
			out[id] = acc
		}
	}
	return out
}

// best finds the longest zone holding name over every account, so a
// delegated sub-zone wins over its parent.
func (a *Accounts) best(name string) (Account, string) {
	var best Account
	zone := ""
	ids := a.State.Keys(Table)
	slices.Sort(ids)
	for _, id := range ids {
		acc, ok := a.get(id)
		if !ok {
			continue
		}
		if z := dnsprovider.ZoneFor(name, acc.Zones); len(z) > len(zone) {
			best, zone = acc, z
		}
	}
	return best, zone
}

// For picks the account and zone for name; a miss rediscovers every
// account's zones, at most once per ResyncEvery.
func (a *Accounts) For(name string) (dnsprovider.Provider, string, error) {
	acc, zone := a.best(name)
	if zone == "" && a.resync() {
		acc, zone = a.best(name)
	}
	if zone == "" {
		return nil, "", fmt.Errorf("%w: %s", ErrNoZone, name)
	}
	p, err := dnsprovider.New(dnsprovider.Config{Type: acc.Type, Zone: zone, Credential: acc.Credential, BaseURL: acc.BaseURL})
	return p, zone, err
}

func (a *Accounts) resync() bool {
	every := a.ResyncEvery
	if every <= 0 {
		every = time.Minute
	}
	a.mu.Lock()
	if time.Since(a.lastResync) < every {
		a.mu.Unlock()
		return false
	}
	a.lastResync = time.Now()
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed := false // Set projects its own write, so best sees it at once
	for _, id := range a.State.Keys(Table) {
		acc, ok := a.get(id)
		if !ok {
			continue
		}
		zones, err := a.discover(ctx, acc)
		if err != nil || slices.Equal(zones, acc.Zones) {
			continue
		}
		if _, err := a.Master.Set(ctx, Table, id, map[string]any{"zones": zones}); err == nil {
			changed = true
		}
	}
	return changed
}
