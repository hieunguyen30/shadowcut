// Package cdc applies Debezium change events from Kafka to Spanner with an
// exactly-once effect (see docs/DECISIONS.md ADR-001/002).
package cdc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
)

// Envelope is a Debezium change event value (JSON converter, schemas disabled).
type Envelope struct {
	Before map[string]any `json:"before"`
	After  map[string]any `json:"after"`
	Op     string         `json:"op"` // c, u, d, r (snapshot read), t (truncate), m (message)
	TsMs   int64          `json:"ts_ms"`
	Source struct {
		TsMs     int64           `json:"ts_ms"`
		LSN      json.Number     `json:"lsn"`
		TxID     json.Number     `json:"txId"`
		Table    string          `json:"table"`
		Schema   string          `json:"schema"`
		Snapshot json.RawMessage `json:"snapshot"`
	} `json:"source"`
}

// Decode parses a record value. A nil/empty value is a tombstone (ok=false).
func Decode(value []byte) (Envelope, bool, error) {
	var e Envelope
	if len(value) == 0 || string(value) == "null" {
		return e, false, nil
	}
	d := json.NewDecoder(bytes.NewReader(value))
	d.UseNumber()
	if err := d.Decode(&e); err != nil {
		return e, false, err
	}
	// Some converter setups wrap the envelope in {"schema":..,"payload":..}.
	if e.Op == "" {
		var wrapped struct {
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(value, &wrapped) == nil && len(wrapped.Payload) > 0 {
			return Decode(wrapped.Payload)
		}
	}
	return e, e.Op != "", nil
}

// SourceTime is the commit time of the change at the source.
func (e Envelope) SourceTime() time.Time {
	if e.Source.TsMs > 0 {
		return time.UnixMilli(e.Source.TsMs).UTC()
	}
	return time.UnixMilli(e.TsMs).UTC()
}

// ColType is a Spanner (PG dialect) column type, as reported by
// information_schema.columns.spanner_type.
type ColType string

// Convert maps a Debezium JSON value to a typed Spanner mutation value.
func Convert(t ColType, v any) (any, error) {
	base := strings.ToLower(string(t))
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	switch base {
	case "bigint":
		if v == nil {
			return spanner.NullInt64{}, nil
		}
		n, err := toInt(v)
		return spanner.NullInt64{Int64: n, Valid: err == nil}, err
	case "double precision", "real":
		if v == nil {
			return spanner.NullFloat64{}, nil
		}
		f, err := toFloat(v)
		if base == "real" {
			return spanner.NullFloat32{Float32: float32(f), Valid: err == nil}, err
		}
		return spanner.NullFloat64{Float64: f, Valid: err == nil}, err
	case "numeric":
		if v == nil {
			return spanner.PGNumeric{}, nil
		}
		return spanner.PGNumeric{Numeric: fmt.Sprint(v), Valid: true}, nil
	case "boolean":
		if v == nil {
			return spanner.NullBool{}, nil
		}
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("want bool, got %T", v)
		}
		return spanner.NullBool{Bool: b, Valid: true}, nil
	case "character varying", "text", "varchar":
		if v == nil {
			return spanner.NullString{}, nil
		}
		return spanner.NullString{StringVal: fmt.Sprint(v), Valid: true}, nil
	case "timestamp with time zone", "timestamptz", "spanner.commit_timestamp":
		if v == nil {
			return spanner.NullTime{}, nil
		}
		s, ok := v.(string)
		if !ok {
			// MicroTimestamp (int64 micros) if time.precision.mode differs
			n, err := toInt(v)
			if err != nil {
				return nil, err
			}
			return spanner.NullTime{Time: time.UnixMicro(n).UTC(), Valid: true}, nil
		}
		ts, err := time.Parse(time.RFC3339Nano, s)
		return spanner.NullTime{Time: ts, Valid: err == nil}, err
	case "date":
		if v == nil {
			return spanner.NullDate{}, nil
		}
		days, err := toInt(v)
		if err != nil {
			return nil, err
		}
		return spanner.NullDate{Date: civil.DateOf(time.Unix(days*86400, 0).UTC()), Valid: true}, nil
	case "jsonb":
		if v == nil {
			return spanner.PGJsonB{}, nil
		}
		s, ok := v.(string)
		if !ok {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		var x any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&x); err != nil {
			return nil, err
		}
		return spanner.PGJsonB{Value: x, Valid: true}, nil
	case "bytea":
		if v == nil {
			return []byte(nil), nil
		}
		s, _ := v.(string)
		return base64.StdEncoding.DecodeString(s)
	}
	return nil, fmt.Errorf("unsupported Spanner column type %q", t)
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case json.Number:
		return x.Int64()
	case float64:
		return int64(x), nil
	case string:
		return strconv.ParseInt(x, 10, 64)
	}
	return 0, fmt.Errorf("want integer, got %T", v)
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case json.Number:
		return x.Float64()
	case float64:
		return x, nil
	case string:
		switch x {
		case "NaN":
			return math.NaN(), nil
		case "Infinity":
			return math.Inf(1), nil
		case "-Infinity":
			return math.Inf(-1), nil
		}
		return strconv.ParseFloat(x, 64)
	}
	return 0, fmt.Errorf("want float, got %T", v)
}
