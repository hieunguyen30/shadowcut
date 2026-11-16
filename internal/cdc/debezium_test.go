package cdc

import (
	"encoding/json"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
)

func TestDecodeEnvelope(t *testing.T) {
	v := []byte(`{"before":null,"after":{"wiki":"enwiki","title":"T","edit_count":3,"avg_delta":"12.3400","last_edit_ts":"2026-11-16T08:36:19.574000Z"},"source":{"ts_ms":1794818477958,"table":"pages","lsn":41043952},"op":"c","ts_ms":1794818478642}`)
	e, ok, err := Decode(v)
	if err != nil || !ok || e.Op != "c" || e.After["title"] != "T" {
		t.Fatalf("%+v %v %v", e, ok, err)
	}
	if got := e.SourceTime(); !got.Equal(time.UnixMilli(1794818477958).UTC()) {
		t.Fatalf("source time %v", got)
	}
	if _, ok, _ := Decode(nil); ok {
		t.Fatal("tombstone should not decode")
	}
}

func TestConvert(t *testing.T) {
	cases := []struct {
		typ  ColType
		in   any
		want any
	}{
		{"bigint", json.Number("42"), spanner.NullInt64{Int64: 42, Valid: true}},
		{"bigint", nil, spanner.NullInt64{}},
		{"numeric", "12.3400", spanner.PGNumeric{Numeric: "12.3400", Valid: true}},
		{"double precision", json.Number("0.5"), spanner.NullFloat64{Float64: 0.5, Valid: true}},
		{"character varying(64)", "x", spanner.NullString{StringVal: "x", Valid: true}},
		{"boolean", true, spanner.NullBool{Bool: true, Valid: true}},
		{"timestamp with time zone", "2026-11-16T08:36:19.574000Z", spanner.NullTime{Time: time.Date(2026, 11, 16, 8, 36, 19, 574000000, time.UTC), Valid: true}},
	}
	for _, c := range cases {
		got, err := Convert(c.typ, c.in)
		if err != nil {
			t.Fatalf("%s %v: %v", c.typ, c.in, err)
		}
		if gt, ok := got.(spanner.NullTime); ok {
			if !gt.Time.Equal(c.want.(spanner.NullTime).Time) {
				t.Fatalf("%s: %v", c.typ, got)
			}
			continue
		}
		if got != c.want {
			t.Fatalf("%s %v: got %#v want %#v", c.typ, c.in, got, c.want)
		}
	}
	if _, err := Convert("jsonb", `{"a": 1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := Convert("interval", "x"); err == nil {
		t.Fatal("unsupported type should error")
	}
}

func TestTableForTopic(t *testing.T) {
	if TableForTopic("sc.public.edits") != "edits" {
		t.Fatal()
	}
}
