package shadow

import "testing"

func TestNamesFirstSourceWins(t *testing.T) {
	m := NamesFromSQL("-- name: app_q\nSELECT a FROM t;", "-- name: curated_q\nselect a  from t;")
	for _, v := range m {
		if v != "app_q" {
			t.Fatalf("got %q", v)
		}
	}
	if len(m) != 1 {
		t.Fatalf("got %d names", len(m))
	}
}
