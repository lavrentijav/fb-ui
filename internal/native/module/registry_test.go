package module_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

type stub struct{ name string }

func (s stub) Name() string                     { return s.name }
func (s stub) Tables() []string                 { return nil }
func (s stub) Mount(module.Router, module.Host) {}
func (s stub) Legacy() []module.LegacyRoute     { return nil }
func (s stub) Run(context.Context, module.Host) {}

func TestRegisterKeepsOrderAndRejectsDuplicateName(t *testing.T) {
	module.Reset()
	t.Cleanup(module.Reset)
	module.Register(stub{"b"})
	module.Register(stub{"a"})
	if got := module.All(); len(got) != 2 || got[0].Name() != "b" || got[1].Name() != "a" {
		t.Fatalf("registration order lost: %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a second module with the same name must panic")
		}
	}()
	module.Register(stub{"a"})
}

func TestModtestHostSetGetDelete(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	if _, err := h.Set(ctx, "t", "k1", map[string]any{"a": 1, "b": "x"}); err != nil {
		t.Fatal(err)
	}
	rec, ok := h.Get("t", "k1")
	if !ok || string(rec["b"]) != `"x"` {
		t.Fatalf("Get = %v %v", rec, ok)
	}
	if keys := h.Keys("t"); len(keys) != 1 || keys[0] != "k1" {
		t.Fatalf("Keys = %v", keys)
	}
	if _, err := h.Delete(ctx, "t", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Get("t", "k1"); ok || len(h.Keys("t")) != 0 {
		t.Fatal("a deleted record must be hidden")
	}
}

func TestEmitWritesEventRecord(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	if err := h.Emit(ctx, module.Event{Type: "node.down", Source: "ru4", At: 42, Data: json.RawMessage(`{"x":1}`)}); err != nil {
		t.Fatal(err)
	}
	keys := h.Keys("event")
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "m1:") || len(keys[0]) != len("m1:")+16 {
		t.Fatalf("event keys = %v", keys)
	}
	rec, _ := h.Get("event", keys[0])
	if string(rec["type"]) != `"node.down"` || string(rec["source"]) != `"ru4"` || string(rec["at"]) != `42` || string(rec["data"]) != `{"x":1}` {
		t.Fatalf("event record = %v", rec)
	}
}
