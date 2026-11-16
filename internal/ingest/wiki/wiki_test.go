package wiki

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSSE(t *testing.T) {
	stream := ":ok\n\nevent: message\nid: [{\"offset\":1}]\ndata: {\"a\":1}\n\ndata: {\"b\":\ndata: 2}\n\nevent: other\ndata: x\n\n"
	var got []string
	var ids []string
	err := ParseSSE(strings.NewReader(stream), func(id, data string) error {
		got = append(got, data)
		ids = append(ids, id)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected stream-closed error, got %v", err)
	}
	if len(got) != 2 || got[0] != `{"a":1}` || got[1] != "{\"b\":\n2}" || ids[0] != `[{"offset":1}]` {
		t.Fatalf("got %q ids %q", got, ids)
	}
}

func TestToEdit(t *testing.T) {
	raw := `{"meta":{"id":"5a1c6c3e-0000-4000-8000-000000000001","dt":"2026-11-16T08:32:53.238Z"},"type":"edit","namespace":0,"title":"Café","user":"192.0.2.1","bot":false,"minor":true,"wiki":"frwiki","length":{"old":100,"new":150},"revision":{"old":1,"new":2}}`
	var ev Event
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	e, ok := ev.ToEdit()
	if !ok || e.UserName != nil || *e.SizeDelta != 50 || *e.GrowthRatio != 1.5 || e.EditTS.Nanosecond() != 238000000 {
		t.Fatalf("%+v", e)
	}
	ev.Type = "log"
	if _, ok := ev.ToEdit(); ok {
		t.Fatal("log events are skipped")
	}
}

func TestIsAnonymous(t *testing.T) {
	for u, want := range map[string]bool{"192.0.2.1": true, "2001:db8::1": true, "~2025-12345-6": true, "Alice": false, "Bot 1.2": false} {
		if IsAnonymous(u) != want {
			t.Errorf("%q", u)
		}
	}
}
