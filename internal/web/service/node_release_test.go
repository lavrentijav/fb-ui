package service

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// Handing ru4 from fi2 to ru2 must leave everything on ru4 in place: a normal
// inbound delete reaches the node, a release must not.
func TestReleaseForgetsANodeWithoutTouchingIt(t *testing.T) {
	setupConflictDB(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"success":true,"obj":[{"id":1,"tag":"in-2053-tcp"},{"id":2,"tag":"in-2083-tcp"}]}`))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }})
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(nil) })

	db := database.GetDB()
	node := &model.Node{
		Name: "ru4", Scheme: "http", Address: "127.0.0.1", Port: p, BasePath: "/", ApiToken: "tok",
		Enable: true, Status: "online", AllowPrivateAddress: true, Role: model.NodeRoleNode,
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	mk := func(tag string, port int) *model.Inbound {
		ib := &model.Inbound{
			Tag: tag, Port: port, Protocol: model.VLESS, Enable: true, NodeID: &node.Id,
			Settings: `{"clients":[],"decryption":"none"}`, StreamSettings: `{"network":"ws"}`,
		}
		if err := db.Create(ib).Error; err != nil {
			t.Fatal(err)
		}
		return ib
	}
	control, kept := mk("in-2053-tcp", 2053), mk("in-2083-tcp", 2083)
	if err := db.Create(&model.Host{GroupId: "g", InboundId: kept.Id, Remark: "FI"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CascadeLink{SourcePanelId: node.Id, SourceInboundTag: kept.Tag, TargetPanelId: 99, TargetInboundId: 7, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}

	var inbounds InboundService
	if _, err := inbounds.DelInbound(control.Id); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == 0 {
		t.Fatal("control: a normal delete never reached the fake node, so this test cannot see a push")
	}
	hits.Store(0)

	var nodes NodeService
	res, err := nodes.Release(node.Id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inbounds != 1 || res.Cascades != 1 {
		t.Fatalf("release = %+v", res)
	}
	if hits.Load() != 0 {
		t.Fatalf("release sent %d request(s) to the node", hits.Load())
	}
	var left int64
	db.Model(&model.Inbound{}).Where("node_id = ?", node.Id).Count(&left)
	var hosts, nodesLeft int64
	db.Model(&model.Host{}).Count(&hosts)
	db.Model(&model.Node{}).Where("id = ?", node.Id).Count(&nodesLeft)
	if left != 0 || hosts != 0 || nodesLeft != 0 {
		t.Fatalf("left behind: inbounds=%d hosts=%d node=%d", left, hosts, nodesLeft)
	}
}
