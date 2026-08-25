package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	xuilogger "github.com/mhsanaei/3x-ui/v3/internal/logger"
)

func newPeerTestDB(t *testing.T) {
	t.Helper()
	xuilogger.InitLogger(logging.ERROR)
	gin.SetMode(gin.TestMode)
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
}

func doPeerReq(t *testing.T, engine *gin.Engine, method, path string, body any) hostEnvelope {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s: status %d, body=%s", method, path, w.Code, w.Body.String())
	}
	var env hostEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s %s: decode envelope: %v body=%s", method, path, err, w.Body.String())
	}
	return env
}

// A master shares model.Node with a controlled node, whose validation demands
// the panel address, port and API token a master row does not have. The peer
// endpoints must not inherit those: the panel's own peer form sends none of them.
func TestPeerController_AddAndUpdateTakeAMasterShapedBody(t *testing.T) {
	newPeerTestDB(t)
	engine := gin.New()
	NewPeerController(engine.Group("/panel/api/peers"))

	body := map[string]any{
		"name": "eu-sub-2", "remark": "", "scheme": "https",
		"subDomain": "sub2.example.com", "subPort": 2096, "subPath": "/sub/", "basePath": "/",
		"subIps": []string{"185.51.100.2"}, "enable": true,
		"allowPrivateAddress": false, "isSelf": false,
	}
	add := doPeerReq(t, engine, http.MethodPost, "/panel/api/peers/add", body)
	if !add.Success {
		t.Fatalf("add rejected a master payload: %s", add.Msg)
	}
	var created model.Node
	if err := json.Unmarshal(add.Obj, &created); err != nil {
		t.Fatalf("decode created peer: %v", err)
	}
	if created.Role != model.NodeRoleMaster || created.SubDomain != "sub2.example.com" {
		t.Fatalf("created = %+v, want a master at sub2.example.com", created)
	}

	body["subIps"] = []string{"185.51.100.2", "185.51.100.3"}
	update := doPeerReq(t, engine, http.MethodPost, "/panel/api/peers/update/"+strconv.Itoa(created.Id), body)
	if !update.Success {
		t.Fatalf("update rejected a master payload: %s", update.Msg)
	}
	list := doPeerReq(t, engine, http.MethodGet, "/panel/api/peers/list", nil)
	var peers []*model.Node
	if err := json.Unmarshal(list.Obj, &peers); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(peers) != 1 || len(peers[0].SubIps) != 2 {
		t.Fatalf("peers = %+v, want the updated static IPs stored", peers)
	}
}
