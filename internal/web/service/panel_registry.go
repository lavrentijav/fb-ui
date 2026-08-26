package service

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// LeaseTTL is how long a claim on the lead is good for. A leader renews well
// inside it; a dead one is replaced one tick after it lapses.
const LeaseTTL = 30 * time.Second

// PanelRegistryService keeps the row this panel owns in the shared `panels`
// table and runs the lease that decides which panel is in charge. Work that
// must happen once per cluster rather than once per panel — the cron jobs that
// reset traffic, reap sessions, poll nodes — asks it who leads.
type PanelRegistryService struct {
	settingService SettingService

	// guid is captured once so an instance keeps standing for the same panel.
	// Left empty it is read from this panel's own setting, which is what every
	// process does; tests set it to act as a second panel on the same database.
	guid string
}

// NewPanelRegistry builds a registry acting for one panel. An empty guid means
// "this panel", read from its panelGuid setting.
func NewPanelRegistry(guid string) *PanelRegistryService {
	return &PanelRegistryService{guid: guid}
}

func (s *PanelRegistryService) identity() (string, error) {
	if s.guid != "" {
		return s.guid, nil
	}
	guid, err := s.settingService.GetPanelGuid()
	if err != nil {
		return "", err
	}
	if guid == "" {
		return "", common.NewError("panel guid is not set yet")
	}
	s.guid = guid
	return guid, nil
}

// Register makes sure this panel has a row and refreshes what it advertises.
// The panelGuid setting is the identity: a rename or a new address updates the
// same row instead of creating a second one.
func (s *PanelRegistryService) Register() (*model.Panel, error) {
	guid, err := s.identity()
	if err != nil {
		return nil, err
	}
	name, _ := s.settingService.GetSubDomain()
	if name == "" {
		name = guid
	}

	db := database.GetDB()
	panel := &model.Panel{
		Guid:     guid,
		Name:     name,
		Address:  name,
		Version:  config.GetPanelVersion(),
		LastSeen: time.Now().Unix(),
	}
	// Insert once, then keep the advertised half fresh. The lease columns are
	// never touched here: registering must not hand anyone the lead.
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "guid"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "address", "version", "last_seen"}),
	}).Create(panel).Error; err != nil {
		return nil, err
	}
	return s.Self()
}

// Self is this panel's row, by the guid it knows itself under.
func (s *PanelRegistryService) Self() (*model.Panel, error) {
	guid, err := s.identity()
	if err != nil {
		return nil, err
	}
	panel := &model.Panel{}
	if err := database.GetDB().Where("guid = ?", guid).First(panel).Error; err != nil {
		return nil, err
	}
	return panel, nil
}

// All lists every panel sharing this database, the leader first.
func (s *PanelRegistryService) All() ([]*model.Panel, error) {
	var panels []*model.Panel
	err := database.GetDB().Model(model.Panel{}).
		Order("leader_until desc").Order("id asc").Find(&panels).Error
	return panels, err
}

// Leader is the panel holding a live lease, if any.
func (s *PanelRegistryService) Leader() (*model.Panel, bool) {
	panel := &model.Panel{}
	err := database.GetDB().Where("leader_until > ?", time.Now().Unix()).
		Order("leader_until desc").First(panel).Error
	if err != nil {
		return nil, false
	}
	return panel, true
}

// Elect renews this panel's lease, or takes the lead when nobody holds it. It
// reports whether this panel leads after the attempt.
//
// The claim is one conditional UPDATE inside a transaction: it only lands when
// the row still looks the way it did when it was read, so two panels racing on
// the same expired lease cannot both win.
func (s *PanelRegistryService) Elect() (bool, error) {
	self, err := s.Self()
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	until := now + int64(LeaseTTL.Seconds())

	leads := false
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		var holder model.Panel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("leader_until > ?", now).Order("leader_until desc").First(&holder).Error
		switch {
		case err == nil && holder.Guid != self.Guid:
			// Somebody else holds a live lease; nothing to do this tick.
			return nil
		case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		term := self.LeaderTerm
		if errors.Is(err, gorm.ErrRecordNotFound) {
			term++ // taking a vacant lead, not renewing our own
		}
		res := tx.Model(model.Panel{}).
			Where("id = ? AND leader_until = ?", self.Id, self.LeaderUntil).
			Updates(map[string]any{"leader_until": until, "leader_term": term, "last_seen": now})
		if res.Error != nil {
			return res.Error
		}
		// A zero row count means another panel moved our row first; it wins.
		leads = res.RowsAffected == 1
		return nil
	})
	return leads, err
}

// Release drops this panel's claim so a shutting-down leader does not hold the
// lead for the rest of its lease.
func (s *PanelRegistryService) Release() error {
	self, err := s.Self()
	if err != nil {
		return err
	}
	return database.GetDB().Model(model.Panel{}).Where("id = ?", self.Id).
		Update("leader_until", 0).Error
}
