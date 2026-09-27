package job

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// FilterListJob refreshes filter lists that follow a source URL. Only the
// lease holder fetches, so a cluster downloads each list once.
type FilterListJob struct {
	registry *service.PanelRegistryService
	filters  service.FilterService
}

func NewFilterListJob(registry *service.PanelRegistryService) *FilterListJob {
	return &FilterListJob{registry: registry}
}

func (j *FilterListJob) Run() {
	if j.registry != nil {
		if leader, ok := j.registry.Leader(); ok {
			if self, err := j.registry.Self(); err != nil || self.Id != leader.Id {
				return
			}
		}
	}
	j.filters.RefreshDueLists(time.Now())
}
