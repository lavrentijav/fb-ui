package service

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/edge"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// MaintenanceRequest takes this panel out of service or brings it back.
type MaintenanceRequest struct {
	Enable      bool `json:"enable" example:"true"`
	IdleSeconds int  `json:"idleSeconds" example:"120"`
}

const maintenanceKey = "maintenance"

var (
	edgeDrainMu sync.Mutex
	edgeDrain   *edge.Drain
)

// SetEdgeDrain is called by the web server that owns the edge; a stored
// maintenance mode is restored so a restart does not reopen a draining node.
func SetEdgeDrain(d *edge.Drain) {
	edgeDrainMu.Lock()
	edgeDrain = d
	edgeDrainMu.Unlock()
	var settings SettingService
	raw, _ := settings.getString(maintenanceKey)
	var req MaintenanceRequest
	if raw != "" && json.Unmarshal([]byte(raw), &req) == nil && req.Enable {
		d.Set(true, time.Duration(req.IdleSeconds)*time.Second)
		logger.Info("maintenance mode: new tunnels are refused")
	}
}

func currentDrain() *edge.Drain {
	edgeDrainMu.Lock()
	defer edgeDrainMu.Unlock()
	return edgeDrain
}

// MaintenanceState is what the edge is doing right now.
func MaintenanceState() edge.DrainState {
	return currentDrain().State()
}

// SetMaintenance stores the mode and applies it to the edge at once.
func SetMaintenance(req MaintenanceRequest) (edge.DrainState, error) {
	if req.IdleSeconds < 0 || req.IdleSeconds > 24*3600 {
		return edge.DrainState{}, common.NewError("idle timeout must be between 0 and 86400 seconds")
	}
	d := currentDrain()
	if d == nil {
		return edge.DrainState{}, common.NewError("the edge is not running in this process")
	}
	b, _ := json.Marshal(req)
	var settings SettingService
	if err := settings.saveSetting(maintenanceKey, string(b)); err != nil {
		return edge.DrainState{}, err
	}
	d.Set(req.Enable, time.Duration(req.IdleSeconds)*time.Second)
	logger.Info("maintenance mode:", req.Enable)
	return d.State(), nil
}
