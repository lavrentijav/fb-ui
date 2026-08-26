package job

import (
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// PanelLeaseJob keeps this panel's row in the shared registry fresh and runs
// the lease that decides which panel is in charge. It ticks well inside the
// lease so a healthy leader never lets it lapse, and a dead one is replaced the
// tick after it does.
type PanelLeaseJob struct {
	registry *service.PanelRegistryService
	running  sync.Mutex
	wasLead  bool
	hadLead  bool
}

func NewPanelLeaseJob(registry *service.PanelRegistryService) *PanelLeaseJob {
	return &PanelLeaseJob{registry: registry}
}

func (j *PanelLeaseJob) Run() {
	if !j.running.TryLock() {
		return
	}
	defer j.running.Unlock()

	if _, err := j.registry.Register(); err != nil {
		logger.Warning("panel registry: register failed:", err)
		return
	}
	leads, err := j.registry.Elect()
	if err != nil {
		logger.Warning("panel registry: election failed:", err)
		return
	}
	// Only log the transitions; a line every tick would bury them.
	if !j.hadLead || leads != j.wasLead {
		if leads {
			logger.Info("panel registry: this panel now leads the cluster")
		} else if leader, ok := j.registry.Leader(); ok {
			logger.Info("panel registry: cluster is led by ", leader.Name)
		} else {
			logger.Warning("panel registry: nobody holds the lead")
		}
		j.wasLead = leads
		j.hadLead = true
	}
}
