package job

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/cluster"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	peerHealthConcurrency    = 16
	peerHealthRequestTimeout = 5 * time.Second
)

// PeerHealthJob keeps the fallback endpoints advertised in subscriptions honest:
// a master that stops answering is dropped from the headers within one tick.
// Only master rows are probed here — a controlled node is reached through its
// panel API by the heartbeat job instead.
type PeerHealthJob struct {
	peerService service.PeerService
	client      *http.Client
	running     sync.Mutex
}

func NewPeerHealthJob() *PeerHealthJob {
	return &PeerHealthJob{client: cluster.NewProbeClient()}
}

func (j *PeerHealthJob) Run() {
	if !j.running.TryLock() {
		return
	}
	defer j.running.Unlock()

	peers, err := j.peerService.GetAll()
	if err != nil {
		logger.Warning("peer health: load peers failed:", err)
		return
	}
	if len(peers) == 0 {
		return
	}

	sem := make(chan struct{}, peerHealthConcurrency)
	var wg sync.WaitGroup
	for _, p := range peers {
		if !p.Enable {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		common.GoRecover("peer-health:"+p.Name, func() {
			defer wg.Done()
			defer func() { <-sem }()
			j.probeOne(p)
		})
	}
	wg.Wait()
}

func (j *PeerHealthJob) probeOne(p *model.Node) {
	ctx, cancel := context.WithTimeout(context.Background(), peerHealthRequestTimeout)
	defer cancel()

	prevStatus := p.Status
	patch := j.peerService.Probe(ctx, j.client, p)
	if err := j.peerService.UpdateHealth(p.Id, patch); err != nil {
		logger.Warning("peer health: update peer", p.Id, "failed:", err)
	}
	publishPeerTransition(p, prevStatus, patch)
}

// publishPeerTransition emits peer.down / peer.up only on a genuine state
// change, so a peer that is simply still offline stays quiet.
func publishPeerTransition(p *model.Node, prevStatus string, patch service.PeerHealthPatch) {
	if EventBus == nil {
		return
	}
	var eventType eventbus.EventType
	switch {
	case prevStatus == "online" && patch.Status == "offline":
		eventType = eventbus.EventPeerDown
	case prevStatus != "online" && patch.Status == "online":
		eventType = eventbus.EventPeerUp
	default:
		return
	}
	source := p.Name
	if source == "" {
		source = "peer-" + strconv.Itoa(p.Id)
	}
	EventBus.Publish(eventbus.Event{
		Type:   eventType,
		Source: source,
		Data: &eventbus.PeerHealthData{
			PeerId:    p.Id,
			Domain:    p.SubDomain,
			LatencyMs: patch.LatencyMs,
			Error:     patch.LastError,
		},
	})
}
