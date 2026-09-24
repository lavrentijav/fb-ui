package job

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// CertRenewJob renews certificates close to expiry and warns about the rest.
// Panels sharing one database would each order the same certificate, so only
// the lease holder runs it.
type CertRenewJob struct {
	registry *service.PanelRegistryService
	certs    service.CertificateService
}

func NewCertRenewJob(registry *service.PanelRegistryService) *CertRenewJob {
	return &CertRenewJob{registry: registry}
}

func (j *CertRenewJob) Run() {
	if j.registry != nil {
		if leader, ok := j.registry.Leader(); ok {
			if self, err := j.registry.Self(); err != nil || self.Id != leader.Id {
				return
			}
		}
	}
	j.certs.RenewDue(time.Now())
}
