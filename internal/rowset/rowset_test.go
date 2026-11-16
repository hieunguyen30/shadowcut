package rowset

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"pgregory.net/rapid"
)

func TestNormalizeEquivalences(t *testing.T) {
	n := pgtype.Numeric{Int: big.NewInt(30000), Exp: -4, Valid: true} // 3.0000
	cases := []struct {
		a, b any
		eq   bool
	}{
		{int32(3), int64(3), true},
		{int64(3), n, true},
		{n, pgtype.Numeric{Int: big.NewInt(3), Exp: 0, Valid: true}, true},
		{[16]byte{0x12, 0x34}, "12340000-0000-0000-0000-000000000000", true},
		{map[string]any{"b": 1.0, "a": []any{1.0, 2.0}}, json.RawMessage(`{"a":[1,2],"b":1}`), true},
		{time.Date(2024, 1, 1, 0, 0, 0, 123456789, time.UTC), time.Date(2023, 12, 31, 16, 0, 0, 123456000, time.FixedZone("PST", -8*3600)), true},
		{runtimeSum(0.1, 0.2), 0.3, false},
		{nil, nil, true},
		{nil, "", false},
		{"a", "A", false},
	}
	for _, c := range cases {
		if got := Equal(Normalize(c.a), Normalize(c.b)); got != c.eq {
			t.Errorf("Equal(%v, %v) = %v, want %v", c.a, c.b, got, c.eq)
		}
	}
}

// Property: Key is consistent with Equal for ints and numerics.
func TestPropKeyConsistent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		i := rapid.Int64Range(-1e12, 1e12).Draw(t, "i")
		scale := rapid.IntRange(0, 6).Draw(t, "scale")
		mul := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
		n := pgtype.Numeric{Int: new(big.Int).Mul(big.NewInt(i), mul), Exp: int32(-scale), Valid: true}
		a, b := Normalize(i), Normalize(n)
		if !Equal(a, b) || a.Key() != b.Key() {
			t.Fatalf("%d vs %v: equal=%v keys %q %q", i, n, Equal(a, b), a.Key(), b.Key())
		}
	})
}

func TestJSONRoundTrip(t *testing.T) {
	vals := []Value{Normalize(nil), Normalize(int64(7)), Normalize(1.5), Normalize("x"), Normalize(true), Normalize(time.Unix(0, 0))}
	for _, v := range vals {
		b, _ := json.Marshal(v)
		var w Value
		if err := json.Unmarshal(b, &w); err != nil || !Equal(v, w) {
			t.Fatalf("round trip %s -> %s -> %v (%v)", v, b, w, err)
		}
	}
}

// runtimeSum defeats Go's exact constant arithmetic (0.1+0.2 == 0.3 at compile time).
func runtimeSum(a, b float64) float64 { return a + b }
