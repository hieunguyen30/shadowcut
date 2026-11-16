// Package probe runs small feature probes against Postgres and Spanner (PGAdapter)
// and records support and result differences. Its output (results/probe/) is the
// experimental evidence cited by the hazard catalog.
package probe

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shadowcut/shadowcut/internal/compare"
	"github.com/shadowcut/shadowcut/internal/rowset"
	"github.com/shadowcut/shadowcut/internal/shadow"
)

// Probe is one feature check.
type Probe struct {
	ID      string
	Area    string // function | syntax | type | ordering | semantics
	Feature string // function name or feature keyword (used by the scanner)
	SQL     string
}

// Setup creates the probe table on both engines (dropped by Teardown).
var Setup = []string{
	`CREATE TABLE sc_probe (id bigint NOT NULL, s text, u text, f double precision, n numeric, ts timestamptz, j jsonb, PRIMARY KEY (id))`,
	`INSERT INTO sc_probe (id, s, u, f, n, ts, j) VALUES
	 (1, 'apple', NULL, 0.1, 1.5, '2024-01-01T00:00:00.123456Z', '{"b": 1, "a": [1, 2]}'),
	 (2, 'Banana', 'x', 0.2, 2.25, '2024-03-10T09:30:00Z', '{"a": 2}'),
	 (3, 'éclair', NULL, 0.3, 3, '2024-06-30T23:59:59.999999Z', NULL),
	 (4, '_under', 'y', 1e-300, NULL, '2024-12-31T16:00:00Z', '[]'),
	 (5, 'Zebra', 'a', NULL, 0.1, '2025-01-01T00:00:00Z', '{"k": "v"}'),
	 (6, 'straße', 'b', 2.5, -2.5, '2024-07-04T12:00:00Z', '{"n": null}'),
	 (7, 'İstanbul', NULL, -0.0, 10.005, '2024-02-29T08:00:00Z', '{"x": 1.0}')`,
}

// Teardown removes the probe table.
var Teardown = []string{`DROP TABLE sc_probe`}

// Probes is the probe set.
var Probes = func() []Probe {
	var ps []Probe
	fn := func(name, expr string) {
		ps = append(ps, Probe{ID: "fn_" + name + fmt.Sprint(len(ps)), Area: "function", Feature: name, SQL: "SELECT " + expr + " AS v FROM sc_probe ORDER BY id"})
	}
	// string functions
	fn("concat", "concat(s, u)")
	fn("concat_ws", "concat_ws('-', s, u)")
	fn("upper", "upper(s)")
	fn("lower", "lower(s)")
	fn("initcap", "initcap(s)")
	fn("length", "length(s)")
	fn("char_length", "char_length(s)")
	fn("octet_length", "octet_length(s)")
	fn("bit_length", "bit_length(s)")
	fn("substr", "substr(s, 2, 3)")
	fn("substring", "substring(s from 2 for 3)")
	fn("left", "left(s, 2)")
	fn("right", "right(s, 2)")
	fn("lpad", "lpad(s, 8, '*')")
	fn("rpad", "rpad(s, 8, '*')")
	fn("trim", "trim(s)")
	fn("btrim", "btrim(s, '_')")
	fn("replace", "replace(s, 'a', 'A')")
	fn("translate", "translate(s, 'ae', 'AE')")
	fn("reverse", "reverse(s)")
	fn("repeat", "repeat(s, 2)")
	fn("position", "position('a' in s)")
	fn("strpos", "strpos(s, 'a')")
	fn("split_part", "split_part(s, 'a', 1)")
	fn("string_to_array", "string_to_array(s, 'a')")
	fn("regexp_replace", "regexp_replace(s, '[aeiou]', '_', 'g')")
	fn("regexp_replace", "regexp_replace(s, '[aeiou]', '_')")
	fn("regexp_match", "regexp_match(s, '[aeiou]+')")
	fn("ascii", "ascii(s)")
	fn("chr", "chr(65)")
	fn("md5", "md5(s)")
	fn("sha256", "sha256(s::bytea)")
	fn("encode", "encode(s::bytea, 'hex')")
	fn("format", "format('%s!', s)")
	fn("quote_ident", "quote_ident(s)")
	fn("to_hex", "to_hex(id)")
	fn("starts_with", "starts_with(s, 'a')")
	// numeric functions
	fn("round", "round(n)")
	fn("round", "round(n, 1)")
	fn("round", "round(f)")
	fn("trunc", "trunc(n)")
	fn("trunc", "trunc(f)")
	fn("ceil", "ceil(f)")
	fn("floor", "floor(n)")
	fn("abs", "abs(n)")
	fn("mod", "mod(id, 3)")
	fn("power", "power(f, 2)")
	fn("sqrt", "sqrt(abs(f))")
	fn("exp", "exp(f)")
	fn("ln", "ln(id::float8)")
	fn("log", "log(id::float8)")
	fn("sign", "sign(n)")
	fn("sign", "sign(f)")
	fn("width_bucket", "width_bucket(f, 0, 1, 4)")
	fn("random", "(random() >= 0)")
	fn("greatest", "greatest(s, 'm')")
	fn("least", "least(n, 1)")
	fn("least", "least(f, 1.0::float8)")
	fn("greatest", "greatest(f, 1.0::float8)")
	fn("nullif", "nullif(u, 'x')")
	fn("coalesce", "coalesce(u, s)")
	// date/time
	fn("now", "(now() > ts)")
	fn("date_trunc", "date_trunc('day', ts)")
	fn("date_trunc", "date_trunc('day', ts, 'UTC')")
	fn("extract", "extract(hour from ts)")
	fn("date_part", "date_part('dow', ts)")
	fn("to_char", "to_char(ts, 'YYYY-MM-DD HH24:MI')")
	fn("age", "age(ts, ts)")
	fn("make_date", "make_date(2024, 1, 1)")
	fn("to_timestamp", "to_timestamp(0)")
	fn("timezone", "timezone('UTC', ts)")
	fn("cast_ts_text", "ts::text")
	fn("cast_ts_date", "ts::date")
	fn("interval_add", "ts + interval '1 day'")
	fn("current_date", "(current_date > '2000-01-01'::date)")
	// json
	fn("jsonb_field", "j->>'a'")
	fn("#>>", "j #>> '{a,0}'")
	fn("jsonb_typeof", "jsonb_typeof(j)")
	fn("jsonb_build_object", "jsonb_build_object('id', id)")
	fn("json_build_object", "json_build_object('id', id)")
	fn("to_jsonb", "to_jsonb(s)")
	fn("jsonb_array_length", "jsonb_array_length(j->'a')")
	fn("jsonb_set", "jsonb_set(j, '{z}', '1'::jsonb)")
	fn("jsonb_contains", "(j @> '{\"a\": 2}'::jsonb)")
	// misc
	fn("gen_random_uuid", "(gen_random_uuid() IS NOT NULL)")
	fn("array_length", "array_length(ARRAY[id, id], 1)")
	fn("array_position", "array_position(ARRAY[1, 2, 3]::bigint[], id)")
	fn("array_to_string", "array_to_string(ARRAY[s, u], ',')")
	fn("array_cat", "array_cat(ARRAY[s], ARRAY[u])")
	fn("cardinality", "cardinality(ARRAY[id])")
	fn("generate_series", "generate_series(1, 2)")
	fn("unnest", "unnest(ARRAY[1, 2])")
	agg := func(name, expr string) {
		ps = append(ps, Probe{ID: "agg_" + name + fmt.Sprint(len(ps)), Area: "function", Feature: name, SQL: "SELECT " + expr + " AS v FROM sc_probe"})
	}
	agg("sum", "sum(f)")
	agg("avg", "avg(f)")
	agg("avg", "avg(n)")
	agg("min", "min(s)")
	agg("max", "max(s)")
	agg("string_agg", "string_agg(s, ',' ORDER BY s)")
	agg("array_agg", "array_agg(s ORDER BY s)")
	agg("count", "count(DISTINCT u)")
	agg("stddev", "stddev(f)")
	agg("variance", "variance(n)")
	agg("bool_and", "bool_and(f > 0)")
	agg("every", "every(f > 0)")
	agg("percentile_cont", "percentile_cont(0.5) WITHIN GROUP (ORDER BY f)")
	agg("mode", "mode() WITHIN GROUP (ORDER BY u)")
	agg("json_agg", "json_agg(s)")
	agg("jsonb_agg", "jsonb_agg(s)")
	syn := func(feature, sql string) {
		ps = append(ps, Probe{ID: "syn_" + feature, Area: "syntax", Feature: feature, SQL: sql})
	}
	syn("order_text", "SELECT s FROM sc_probe ORDER BY s")
	syn("order_text_desc", "SELECT s FROM sc_probe ORDER BY s DESC")
	syn("order_nulls_default_asc", "SELECT id, u FROM sc_probe ORDER BY u, id")
	syn("order_nulls_default_desc", "SELECT id, u FROM sc_probe ORDER BY u DESC, id")
	syn("order_nulls_first", "SELECT id, u FROM sc_probe ORDER BY u NULLS FIRST, id")
	syn("text_compare", "SELECT id FROM sc_probe WHERE s > 'a' ORDER BY id")
	syn("distinct_on", "SELECT DISTINCT ON (u) u, id FROM sc_probe ORDER BY u, id")
	syn("row_compare", "SELECT id FROM sc_probe WHERE (id, s) < (3, 'a') ORDER BY id")
	syn("collate", "SELECT s FROM sc_probe ORDER BY s COLLATE \"C\"")
	syn("any_array_literal", "SELECT id FROM sc_probe WHERE id = ANY('{1,2}') ORDER BY id")
	syn("any_array_ctor", "SELECT id FROM sc_probe WHERE id = ANY(ARRAY[1, 2]) ORDER BY id")
	syn("in_list", "SELECT id FROM sc_probe WHERE id IN (1, 2) ORDER BY id")
	syn("ilike", "SELECT id FROM sc_probe WHERE s ILIKE 'a%' ORDER BY id")
	syn("regex_match", "SELECT id FROM sc_probe WHERE s ~ '^[a-z]' ORDER BY id")
	syn("similar_to", "SELECT id FROM sc_probe WHERE s SIMILAR TO '[a-z]%' ORDER BY id")
	syn("window_row_number", "SELECT id, row_number() OVER (ORDER BY id) AS rn FROM sc_probe ORDER BY id")
	syn("window_sum", "SELECT id, sum(f) OVER (PARTITION BY u) AS w FROM sc_probe ORDER BY id")
	syn("recursive_cte", "WITH RECURSIVE r(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM r WHERE i < 3) SELECT i FROM r")
	syn("cte", "WITH c AS (SELECT id FROM sc_probe) SELECT count(*) FROM c")
	syn("lateral", "SELECT p.id, x.v FROM sc_probe p, LATERAL (SELECT p.id * 2 AS v) x ORDER BY p.id")
	syn("full_join", "SELECT a.id FROM sc_probe a FULL JOIN sc_probe b ON a.id = b.id ORDER BY a.id")
	syn("fetch_first", "SELECT id FROM sc_probe ORDER BY id FETCH FIRST 2 ROWS ONLY")
	syn("tablesample", "SELECT count(*) >= 0 FROM sc_probe TABLESAMPLE BERNOULLI (50)")
	syn("for_update_autocommit", "SELECT id FROM sc_probe ORDER BY id LIMIT 1 FOR UPDATE")
	syn("skip_locked", "SELECT id FROM sc_probe ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED")
	syn("group_by_rollup", "SELECT u, count(*) FROM sc_probe GROUP BY ROLLUP (u)")
	syn("having", "SELECT u, count(*) FROM sc_probe GROUP BY u HAVING count(*) > 1")
	syn("exists_subquery", "SELECT id FROM sc_probe p WHERE EXISTS (SELECT 1 FROM sc_probe q WHERE q.id = p.id + 1) ORDER BY id")
	syn("union", "SELECT id FROM sc_probe UNION SELECT id + 10 FROM sc_probe ORDER BY 1")
	syn("int_division", "SELECT id, id / 2 AS h, -id / 2 AS nh, id % 3 AS m FROM sc_probe ORDER BY id")
	syn("float_division_zero", "SELECT 1.0::float8 / 0")
	syn("numeric_division", "SELECT n / 3 AS v FROM sc_probe ORDER BY id")
	syn("float_text", "SELECT f::text FROM sc_probe ORDER BY id")
	syn("numeric_text", "SELECT n::text FROM sc_probe ORDER BY id")
	syn("bool_text", "SELECT (f > 0)::text FROM sc_probe ORDER BY id")
	syn("jsonb_text", "SELECT j::text FROM sc_probe ORDER BY id")
	syn("timestamp_without_tz", "SELECT ts::timestamp FROM sc_probe ORDER BY id")
	syn("int4_overflow", "SELECT 2147483647::int4 + 1")
	syn("show_timezone", "SHOW timezone")
	return ps
}()

// Result of one probe.
type Result struct {
	Probe
	PGErr, SPErr string
	PGOut, SPOut string
	Status       string // same | differs | unsupported | pg_error | both_error
}

// Run executes all probes.
func Run(ctx context.Context, pg, sp *pgxpool.Pool) ([]Result, error) {
	for _, s := range Teardown {
		pg.Exec(ctx, s)
		sp.Exec(ctx, s)
	}
	for _, s := range Setup {
		if _, err := pg.Exec(ctx, s); err != nil {
			return nil, fmt.Errorf("postgres setup: %w", err)
		}
		if _, err := sp.Exec(ctx, s); err != nil {
			return nil, fmt.Errorf("spanner setup: %w", err)
		}
	}
	defer func() {
		for _, s := range Teardown {
			pg.Exec(context.Background(), s)
			sp.Exec(context.Background(), s)
		}
	}()
	var out []Result
	for _, p := range Probes {
		a := shadow.Exec(ctx, pg, p.SQL, nil, false, 20*time.Second)
		b := shadow.Exec(ctx, sp, p.SQL, nil, false, 20*time.Second)
		r := Result{Probe: p, PGErr: a.Err, SPErr: b.Err, PGOut: render(a), SPOut: render(b)}
		switch {
		case a.Err != "" && b.Err != "":
			r.Status = "both_error"
		case a.Err != "":
			r.Status = "pg_error"
		case b.Err != "":
			r.Status = "unsupported"
		case compare.Compare(a, b, compare.Ordered).Equal():
			r.Status = "same"
		default:
			r.Status = "differs"
		}
		out = append(out, r)
	}
	return out, nil
}

func render(r *rowset.Result) string {
	if r.Err != "" {
		return "ERROR: " + r.Err
	}
	var rows []string
	for _, row := range r.Rows {
		var cells []string
		for _, v := range row {
			cells = append(cells, v.String())
		}
		rows = append(rows, strings.Join(cells, "|"))
	}
	return strings.Join(rows, "; ")
}

// Write saves results as CSV and Markdown into dir.
func Write(dir string, rs []Result, env string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "probe.csv"))
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"id", "area", "feature", "status", "sql", "postgres", "spanner"})
	for _, r := range rs {
		w.Write([]string{r.ID, r.Area, r.Feature, r.Status, r.SQL, r.PGOut, r.SPOut})
	}
	w.Flush()
	f.Close()
	counts := map[string]int{}
	for _, r := range rs {
		counts[r.Status]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Feature probe: PostgreSQL 16 vs Spanner PostgreSQL interface\n\nEnvironment: %s. Generated %s by `sc-exp probe`.\n\n", env, time.Now().UTC().Format(time.RFC3339))
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "- **%s**: %d\n", k, counts[k])
	}
	b.WriteString("\n| id | feature | status | Postgres | Spanner |\n|---|---|---|---|---|\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s |\n", r.ID, r.Feature, r.Status, mdCell(r.PGOut), mdCell(r.SPOut))
	}
	return os.WriteFile(filepath.Join(dir, "probe.md"), []byte(b.String()), 0o644)
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > 90 {
		s = string(r[:87]) + "..."
	}
	return s
}

// WriteEvidence writes the compact evidence table embedded by internal/hazard.
func WriteEvidence(path string, rs []Result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"id", "area", "feature", "status"})
	for _, r := range rs {
		w.Write([]string{r.ID, r.Area, r.Feature, r.Status})
	}
	w.Flush()
	return w.Error()
}
