// Package rowset normalizes query results from PostgreSQL and from Spanner
// (via PGAdapter) into a common, comparable representation.
//
// Normalization rules (see docs/DECISIONS.md ADR-004, "comparison semantics"):
//   - integers of any width compare equal by value (Spanner widens int4 -> int8)
//   - numerics compare by exact rational value (scale/trailing zeros are display only)
//   - floats compare exactly by default; a tolerance is only used by the classifier
//     to *explain* a mismatch as "precision", never to hide one
//   - timestamps compare as UTC instants at microsecond precision
//   - uuid and text compare as strings (Spanner schemas often map uuid -> varchar(36))
//   - jsonb compares structurally (key order is irrelevant)
package rowset

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Kind is the normalized value class.
type Kind uint8

const (
	Null Kind = iota
	Bool
	Int
	Float
	Numeric
	Text
	Time
	Bytes
	JSON
	Other
)

func (k Kind) String() string {
	return [...]string{"null", "bool", "int", "float", "numeric", "text", "time", "bytes", "json", "other"}[k]
}

// Value is one normalized cell.
type Value struct {
	K Kind
	B bool
	I int64
	F float64
	N *big.Rat // Numeric (nil for NaN)
	S string   // Text, JSON (canonical), Other, Numeric NaN marker
	T time.Time
	X []byte
}

// Row is one result row.
type Row []Value

// Column describes a result column.
type Column struct {
	Name string `json:"name"`
	OID  uint32 `json:"oid,omitempty"`
}

// Result is a complete query result (or error).
type Result struct {
	Columns []Column
	Rows    []Row
	Err     string        // non-empty if the query failed
	Elapsed time.Duration // wall time to produce the result
}

// IsNull reports whether v is NULL.
func (v Value) IsNull() bool { return v.K == Null }

// Normalize converts a Go value as produced by pgx (rows.Values() or
// pgtype.Map.DecodeValue) into a Value.
func Normalize(x any) Value {
	switch v := x.(type) {
	case nil:
		return Value{K: Null}
	case bool:
		return Value{K: Bool, B: v}
	case int:
		return Value{K: Int, I: int64(v)}
	case int8:
		return Value{K: Int, I: int64(v)}
	case int16:
		return Value{K: Int, I: int64(v)}
	case int32:
		return Value{K: Int, I: int64(v)}
	case int64:
		return Value{K: Int, I: v}
	case uint32:
		return Value{K: Int, I: int64(v)}
	case float32:
		return Value{K: Float, F: float64(v)}
	case float64:
		return Value{K: Float, F: v}
	case string:
		return Value{K: Text, S: v}
	case []byte:
		return Value{K: Bytes, X: v}
	case time.Time:
		return Value{K: Time, T: v.UTC().Truncate(time.Microsecond)}
	case [16]byte:
		return Value{K: Text, S: uuidString(v)}
	case pgtype.UUID:
		if !v.Valid {
			return Value{K: Null}
		}
		return Value{K: Text, S: uuidString(v.Bytes)}
	case pgtype.Numeric:
		return numericValue(v)
	case pgtype.Text:
		if !v.Valid {
			return Value{K: Null}
		}
		return Value{K: Text, S: v.String}
	case pgtype.Timestamptz:
		if !v.Valid {
			return Value{K: Null}
		}
		return Value{K: Time, T: v.Time.UTC().Truncate(time.Microsecond)}
	case pgtype.Date:
		if !v.Valid {
			return Value{K: Null}
		}
		return Value{K: Time, T: v.Time.UTC()}
	case pgtype.Interval:
		if !v.Valid {
			return Value{K: Null}
		}
		return Value{K: Other, S: fmt.Sprintf("interval(%dmon,%dd,%dus)", v.Months, v.Days, v.Microseconds)}
	case netip.Prefix:
		return Value{K: Text, S: v.String()}
	case map[string]any, []any:
		return jsonValue(v)
	case json.RawMessage:
		var y any
		if err := json.Unmarshal(v, &y); err != nil {
			return Value{K: Text, S: string(v)}
		}
		return jsonValue(y)
	case fmt.Stringer:
		return Value{K: Other, S: v.String()}
	default:
		return Value{K: Other, S: fmt.Sprintf("%v", v)}
	}
}

func uuidString(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func numericValue(n pgtype.Numeric) Value {
	if !n.Valid {
		return Value{K: Null}
	}
	if n.NaN {
		return Value{K: Numeric, S: "NaN"}
	}
	if n.InfinityModifier != pgtype.Finite {
		return Value{K: Numeric, S: fmt.Sprint(n.InfinityModifier)}
	}
	r := new(big.Rat).SetInt(n.Int)
	if n.Exp > 0 {
		r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Exp)), nil)))
	} else if n.Exp < 0 {
		r.Quo(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-n.Exp)), nil)))
	}
	return Value{K: Numeric, N: r}
}

// NumericFromString parses a decimal string into a Numeric value.
func NumericFromString(s string) (Value, bool) {
	if s == "NaN" {
		return Value{K: Numeric, S: "NaN"}, true
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return Value{}, false
	}
	return Value{K: Numeric, N: r}, true
}

func jsonValue(v any) Value {
	var b bytes.Buffer
	canonJSON(&b, v)
	return Value{K: JSON, S: b.String()}
}

func canonJSON(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			canonJSON(b, x[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			canonJSON(b, e)
		}
		b.WriteByte(']')
	case float64:
		// JSON numbers: canonical shortest representation
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	default:
		e, _ := json.Marshal(x)
		b.Write(e)
	}
}

// Equal reports exact equality under the normalization rules.
func Equal(a, b Value) bool {
	if a.K == Null || b.K == Null {
		return a.K == b.K
	}
	// cross-kind numeric comparison: int vs numeric (e.g. count(*) types differ)
	if a.K != b.K {
		ra, oka := a.Rat()
		rb, okb := b.Rat()
		if oka && okb && (a.K != Float && b.K != Float) {
			return ra.Cmp(rb) == 0
		}
		// text vs json/other: compare rendered strings
		if (a.K == Text || a.K == Other) && (b.K == Text || b.K == Other) {
			return a.S == b.S
		}
		return false
	}
	switch a.K {
	case Bool:
		return a.B == b.B
	case Int:
		return a.I == b.I
	case Float:
		if math.IsNaN(a.F) && math.IsNaN(b.F) {
			return true
		}
		return a.F == b.F
	case Numeric:
		if a.N == nil || b.N == nil {
			return a.S == b.S && a.N == nil && b.N == nil
		}
		return a.N.Cmp(b.N) == 0
	case Time:
		return a.T.Equal(b.T)
	case Bytes:
		return bytes.Equal(a.X, b.X)
	default:
		return a.S == b.S
	}
}

// Rat returns the exact rational value of a numeric-ish value.
func (v Value) Rat() (*big.Rat, bool) {
	switch v.K {
	case Int:
		return new(big.Rat).SetInt64(v.I), true
	case Numeric:
		if v.N == nil {
			return nil, false
		}
		return v.N, true
	case Float:
		if math.IsNaN(v.F) || math.IsInf(v.F, 0) {
			return nil, false
		}
		r := new(big.Rat)
		r.SetFloat64(v.F)
		return r, true
	}
	return nil, false
}

// Float64 returns an approximate float for numeric-ish values.
func (v Value) Float64() (float64, bool) {
	switch v.K {
	case Int:
		return float64(v.I), true
	case Float:
		return v.F, true
	case Numeric:
		if v.N == nil {
			return 0, false
		}
		f, _ := v.N.Float64()
		return f, true
	}
	return 0, false
}

// IsNumber reports whether v is int, float or numeric.
func (v Value) IsNumber() bool { return v.K == Int || v.K == Float || v.K == Numeric }

// String renders v for logs and keys. Distinct values render distinctly under Equal
// for same-kind values (ints and integral numerics share a rendering on purpose).
func (v Value) String() string {
	switch v.K {
	case Null:
		return "NULL"
	case Bool:
		return strconv.FormatBool(v.B)
	case Int:
		return strconv.FormatInt(v.I, 10)
	case Float:
		return strconv.FormatFloat(v.F, 'g', -1, 64)
	case Numeric:
		if v.N == nil {
			return v.S
		}
		if v.N.IsInt() {
			return v.N.Num().String()
		}
		return strings.TrimRight(strings.TrimRight(v.N.FloatString(40), "0"), ".")
	case Time:
		return v.T.Format("2006-01-02T15:04:05.000000Z07:00")
	case Bytes:
		return `\x` + hex.EncodeToString(v.X)
	default:
		return v.S
	}
}

// Key renders a stable identity string for a value that is consistent with Equal
// for non-float values. Ints and integral numerics share the same key.
func (v Value) Key() string {
	switch v.K {
	case Int, Numeric:
		return "n:" + v.String()
	case Float:
		return "f:" + v.String()
	case Text, Other:
		return "s:" + v.S
	default:
		return v.K.String() + ":" + v.String()
	}
}

// RowKey renders a row identity for multiset comparison.
func RowKey(r Row) string {
	var b strings.Builder
	for i, v := range r {
		if i > 0 {
			b.WriteByte('\x1f')
		}
		b.WriteString(v.Key())
	}
	return b.String()
}

// RowsEqual compares two rows cell by cell.
func RowsEqual(a, b Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// MarshalJSON renders a value for mismatch logs.
func (v Value) MarshalJSON() ([]byte, error) {
	if v.K == Null {
		return []byte("null"), nil
	}
	return json.Marshal(map[string]string{"k": v.K.String(), "v": v.String()})
}

// UnmarshalJSON parses the log form back (used by report/replay tooling).
func (v *Value) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*v = Value{K: Null}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	s := m["v"]
	switch m["k"] {
	case "bool":
		*v = Value{K: Bool, B: s == "true"}
	case "int":
		i, _ := strconv.ParseInt(s, 10, 64)
		*v = Value{K: Int, I: i}
	case "float":
		f, _ := strconv.ParseFloat(s, 64)
		*v = Value{K: Float, F: f}
	case "numeric":
		nv, ok := NumericFromString(s)
		if !ok {
			nv = Value{K: Numeric, S: s}
		}
		*v = nv
	case "time":
		t, _ := time.Parse("2006-01-02T15:04:05.000000Z07:00", s)
		*v = Value{K: Time, T: t}
	case "bytes":
		x, _ := hex.DecodeString(strings.TrimPrefix(s, `\x`))
		*v = Value{K: Bytes, X: x}
	case "json":
		*v = Value{K: JSON, S: s}
	case "text":
		*v = Value{K: Text, S: s}
	default:
		*v = Value{K: Other, S: s}
	}
	return nil
}
