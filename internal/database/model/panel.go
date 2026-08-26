package model

// Panel is one panel sharing this database, and which of them is in charge.
//
// Leadership is a lease, not a flag: LeaderUntil is a deadline the leader keeps
// pushing forward. A panel that dies stops renewing and the next tick lets
// another one take over, where a plain "is_leader" column would leave the
// cluster leaderless until a human noticed.
type Panel struct {
	Id int `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`

	// Guid is the panel's own stable identifier (its panelGuid setting), which
	// is how a panel finds its own row again across restarts and renames.
	Guid string `json:"guid" gorm:"column:guid;uniqueIndex" example:"7f3a1c02-..."`

	Name    string `json:"name" form:"name" example:"msk1-moscow"`
	Address string `json:"address" form:"address" example:"msk1.example.com"`
	Version string `json:"version" gorm:"column:version" example:"v3.2.0"`

	// LeaderUntil is when this panel's claim on the lead expires, in unix
	// seconds. The leader is the row whose deadline has not passed yet; at most
	// one row can hold a live one.
	LeaderUntil int64 `json:"leaderUntil" gorm:"column:leader_until;default:0" example:"1700000030"`

	// LeaderTerm counts handovers, so a log can tell a renewal from a takeover.
	LeaderTerm int64 `json:"leaderTerm" gorm:"column:leader_term;default:0" example:"3"`

	LastSeen  int64 `json:"lastSeen" gorm:"column:last_seen" example:"1700000000"`
	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt int64 `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (Panel) TableName() string { return "panels" }

// IsLeader reports whether this row holds the lead at the given moment.
func (p Panel) IsLeader(now int64) bool {
	return p.LeaderUntil > now
}
