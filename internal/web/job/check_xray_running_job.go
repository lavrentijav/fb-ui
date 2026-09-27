// Package job provides background job implementations for the 3x-ui web panel,
// including traffic monitoring, system checks, and periodic maintenance tasks.
package job

import (
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// EventBus is set from web layer to publish events.
var EventBus *eventbus.Bus

// CheckXrayRunningJob monitors Xray process health and restarts it if it crashes.
type CheckXrayRunningJob struct {
	xrayService service.XrayService
	checkTime   int
	lostTime    int
	lastProbe   time.Time
}

const listenerProbeEvery = 30 * time.Second

// NewCheckXrayRunningJob creates a new Xray health check job instance.
func NewCheckXrayRunningJob() *CheckXrayRunningJob {
	return new(CheckXrayRunningJob)
}

// Run checks if Xray has crashed and restarts it after confirming it's down for 2 consecutive checks.
func (j *CheckXrayRunningJob) Run() {
	if !j.xrayService.DidXrayCrash() {
		j.checkTime = 0
		j.checkListeners()
	} else {
		j.checkTime++
		// only restart if it's down 2 times in a row
		if j.checkTime > 1 {
			err := j.xrayService.RestartXray(false)
			j.checkTime = 0
			if err != nil {
				logger.Error("Restart xray failed:", err)
			}
		}
	}
}

// checkListeners restarts a core that runs but has lost an inbound's port,
// once the loss is seen on two checks in a row (a hot apply is in between).
func (j *CheckXrayRunningJob) checkListeners() {
	if time.Since(j.lastProbe) < listenerProbeEvery {
		return
	}
	j.lastProbe = time.Now()
	lost := j.xrayService.LostListeners()
	if len(lost) == 0 {
		j.lostTime = 0
		return
	}
	j.lostTime++
	if j.lostTime < 2 {
		return
	}
	j.lostTime = 0
	logger.Error("xray: inbound ports stopped listening, restarting the core:", strings.Join(lost, ", "))
	if err := j.xrayService.RestartXray(true); err != nil {
		logger.Error("Restart xray failed:", err)
	}
}
