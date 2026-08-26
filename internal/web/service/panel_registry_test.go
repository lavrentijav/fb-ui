package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func initPanelRegistryDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	if err := database.InitDB(filepath.Join(dir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	if err := database.GetDB().Where("1 = 1").Delete(&model.Panel{}).Error; err != nil {
		t.Fatalf("clear panels: %v", err)
	}
}

// asPanel runs fn as the panel with this guid: two panels sharing one database
// differ only by the panelGuid each process knows itself under.
func asPanel(t *testing.T, guid string) *PanelRegistryService {
	t.Helper()
	registry := NewPanelRegistry(guid)
	if _, err := registry.Register(); err != nil {
		t.Fatalf("Register(%s): %v", guid, err)
	}
	return registry
}

func TestPanelRegistrationIsIdempotent(t *testing.T) {
	initPanelRegistryDB(t)
	first := asPanel(t, "guid-a")
	self, err := first.Self()
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	// Re-registering is what every restart does; it must find the same row.
	again := asPanel(t, "guid-a")
	stored, err := again.Self()
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if stored.Id != self.Id {
		t.Fatalf("panel id = %d after re-registering, want the original %d", stored.Id, self.Id)
	}
	panels, err := again.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(panels) != 1 {
		t.Fatalf("panels = %d, want one row per guid", len(panels))
	}
}

// The point of the table: exactly one panel is in charge, and the others know
// it without asking anyone.
func TestOnlyOnePanelHoldsTheLead(t *testing.T) {
	initPanelRegistryDB(t)
	a := asPanel(t, "guid-a")
	if leads, err := a.Elect(); err != nil || !leads {
		t.Fatalf("Elect on a vacant lead = %v, %v; want it taken", leads, err)
	}

	b := asPanel(t, "guid-b")
	leads, err := b.Elect()
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if leads {
		t.Fatal("a second panel took the lead while the first one still holds it")
	}
	leader, ok := b.Leader()
	if !ok || leader.Guid != "guid-a" {
		t.Fatalf("leader = %+v, want guid-a", leader)
	}

	// Renewing is not a handover: the term only moves when the lead changes hands.
	before, _ := a.Self()
	if leads, err := a.Elect(); err != nil || !leads {
		t.Fatalf("the leader failed to renew: %v, %v", leads, err)
	}
	after, _ := a.Self()
	if after.LeaderTerm != before.LeaderTerm {
		t.Fatalf("term = %d after a renewal, want it unchanged at %d", after.LeaderTerm, before.LeaderTerm)
	}
	if after.LeaderUntil < before.LeaderUntil {
		t.Fatal("renewing moved the deadline backwards")
	}
}

// A leader that dies stops renewing; the lease is what lets the next one in.
func TestALapsedLeaseIsTakenOver(t *testing.T) {
	initPanelRegistryDB(t)
	a := asPanel(t, "guid-a")
	if _, err := a.Elect(); err != nil {
		t.Fatalf("Elect: %v", err)
	}
	b := asPanel(t, "guid-b")
	if leads, _ := b.Elect(); leads {
		t.Fatal("took the lead from a live lease")
	}

	// Wind the holder's deadline into the past, as a stopped panel would.
	expired := time.Now().Unix() - 1
	if err := database.GetDB().Model(model.Panel{}).Where("guid = ?", "guid-a").
		Update("leader_until", expired).Error; err != nil {
		t.Fatalf("expire the lease: %v", err)
	}

	leads, err := b.Elect()
	if err != nil || !leads {
		t.Fatalf("Elect after the lease lapsed = %v, %v; want the takeover", leads, err)
	}
	leader, ok := b.Leader()
	if !ok || leader.Guid != "guid-b" {
		t.Fatalf("leader = %+v, want guid-b", leader)
	}
	self, _ := b.Self()
	if self.LeaderTerm != 1 {
		t.Fatalf("term = %d after taking a vacant lead, want it counted", self.LeaderTerm)
	}

	// And the old leader must not think it still leads.
	if _, ok := a.Leader(); !ok {
		t.Fatal("Leader() lost the new holder")
	}
	if leads, _ := a.Elect(); leads {
		t.Fatal("the lapsed leader took the lead back from under the new one")
	}
}

// A panel shutting down should not keep the lead for the rest of its lease.
func TestReleaseFreesTheLeadImmediately(t *testing.T) {
	initPanelRegistryDB(t)
	a := asPanel(t, "guid-a")
	if _, err := a.Elect(); err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if leader, ok := a.Leader(); ok {
		t.Fatalf("leader = %+v after release, want the lead vacant", leader)
	}
	b := asPanel(t, "guid-b")
	if leads, err := b.Elect(); err != nil || !leads {
		t.Fatalf("Elect after release = %v, %v; want the lead taken", leads, err)
	}
}
