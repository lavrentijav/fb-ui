package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// ManagedRoutingJob pushes each node the routing its master owns for it.
// Panels sharing one database would push the same thing, so only the lease
// holder runs it.
type ManagedRoutingJob struct {
	registry *service.PanelRegistryService
}

func NewManagedRoutingJob(registry *service.PanelRegistryService) *ManagedRoutingJob {
	return &ManagedRoutingJob{registry: registry}
}

func (j *ManagedRoutingJob) Run() {
	if j.registry != nil {
		if leader, ok := j.registry.Leader(); ok {
			if self, err := j.registry.Self(); err != nil || self.Id != leader.Id {
				return
			}
		}
	}
	service.SyncManagedRouting()
}
