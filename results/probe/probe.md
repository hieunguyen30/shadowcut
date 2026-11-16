# Feature probe: PostgreSQL 16 vs Spanner PostgreSQL interface

Environment: local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner). Generated 2026-11-16T09:03:07Z by `sc-exp probe`.

- **both_error**: 4
- **differs**: 21
- **pg_error**: 1
- **same**: 68
- **unsupported**: 53

| id | feature | status | Postgres | Spanner |
|---|---|---|---|---|
| fn_concat0 | `concat` | differs | apple; Bananax; éclair; _undery; Zebraa; straßeb; İstanbul | NULL; Bananax; NULL; _undery; Zebraa; straßeb; NULL |
| fn_concat_ws1 | `concat_ws` | unsupported | apple; Banana-x; éclair; _under-y; Zebra-a; straße-b; İstanbul | ERROR: Postgres function concat_ws(text, text, text) is not supported |
| fn_upper2 | `upper` | differs | APPLE; BANANA; ÉCLAIR; _UNDER; ZEBRA; STRAßE; İSTANBUL | APPLE; BANANA; ÉCLAIR; _UNDER; ZEBRA; STRASSE; İSTANBUL |
| fn_lower3 | `lower` | differs | apple; banana; éclair; _under; zebra; straße; istanbul | apple; banana; éclair; _under; zebra; straße; i̇stanbul |
| fn_initcap4 | `initcap` | unsupported | Apple; Banana; Éclair; _Under; Zebra; Straße; İstanbul | ERROR: Postgres function initcap(text) is not supported |
| fn_length5 | `length` | same | 5; 6; 6; 6; 5; 6; 8 | 5; 6; 6; 6; 5; 6; 8 |
| fn_char_length6 | `char_length` | unsupported | 5; 6; 6; 6; 5; 6; 8 | ERROR: Postgres function char_length(text) is not supported |
| fn_octet_length7 | `octet_length` | unsupported | 5; 6; 7; 6; 5; 7; 9 | ERROR: Postgres function octet_length(text) is not supported |
| fn_bit_length8 | `bit_length` | unsupported | 40; 48; 56; 48; 40; 56; 72 | ERROR: Postgres function bit_length(text) is not supported |
| fn_substr9 | `substr` | same | ppl; ana; cla; und; ebr; tra; sta | ppl; ana; cla; und; ebr; tra; sta |
| fn_substring10 | `substring` | same | ppl; ana; cla; und; ebr; tra; sta | ppl; ana; cla; und; ebr; tra; sta |
| fn_left11 | `left` | unsupported | ap; Ba; éc; _u; Ze; st; İs | ERROR: Postgres function left(text, bigint) is not supported |
| fn_right12 | `right` | unsupported | le; na; ir; er; ra; ße; ul | ERROR: Postgres function right(text, bigint) is not supported |
| fn_lpad13 | `lpad` | same | ***apple; **Banana; **éclair; **_under; ***Zebra; **straße; İstanbul | ***apple; **Banana; **éclair; **_under; ***Zebra; **straße; İstanbul |
| fn_rpad14 | `rpad` | same | apple***; Banana**; éclair**; _under**; Zebra***; straße**; İstanbul | apple***; Banana**; éclair**; _under**; Zebra***; straße**; İstanbul |
| fn_trim15 | `trim` | same | apple; Banana; éclair; _under; Zebra; straße; İstanbul | apple; Banana; éclair; _under; Zebra; straße; İstanbul |
| fn_btrim16 | `btrim` | same | apple; Banana; éclair; under; Zebra; straße; İstanbul | apple; Banana; éclair; under; Zebra; straße; İstanbul |
| fn_replace17 | `replace` | same | Apple; BAnAnA; éclAir; _under; ZebrA; strAße; İstAnbul | Apple; BAnAnA; éclAir; _under; ZebrA; strAße; İstAnbul |
| fn_translate18 | `translate` | unsupported | ApplE; BAnAnA; éclAir; _undEr; ZEbrA; strAßE; İstAnbul | ERROR: Postgres function translate(text, text, text) is not supported |
| fn_reverse19 | `reverse` | same | elppa; ananaB; rialcé; rednu_; arbeZ; eßarts; lubnatsİ | elppa; ananaB; rialcé; rednu_; arbeZ; eßarts; lubnatsİ |
| fn_repeat20 | `repeat` | same | appleapple; BananaBanana; éclairéclair; _under_under; ZebraZebra; straßestraße; İstanbu... | appleapple; BananaBanana; éclairéclair; _under_under; ZebraZebra; straßestraße; İstanbu... |
| fn_position21 | `position` | unsupported | 1; 2; 4; 0; 5; 4; 4 | ERROR: Postgres function position(text, text) is not supported |
| fn_strpos22 | `strpos` | same | 1; 2; 4; 0; 5; 4; 4 | 1; 2; 4; 0; 5; 4; 4 |
| fn_split_part23 | `split_part` | unsupported | ; B; écl; _under; Zebr; str; İst | ERROR: function split_part(text, unknown, bigint) does not exist Hint: No function matc... |
| fn_string_to_array24 | `string_to_array` | unsupported | ["","pple"]; ["B","n","n",""]; ["écl","ir"]; ["_under"]; ["Zebr",""]; ["str","ße"]; ["İ... | ERROR: Postgres function string_to_array(text, text) is not supported |
| fn_regexp_replace25 | `regexp_replace` | unsupported | _ppl_; B_n_n_; écl__r; __nd_r; Z_br_; str_ß_; İst_nb_l | ERROR: Postgres function regexp_replace(text, text, text, text) is not supported |
| fn_regexp_replace26 | `regexp_replace` | differs | _pple; B_nana; écl_ir; __nder; Z_bra; str_ße; İst_nbul | _ppl_; B_n_n_; écl__r; __nd_r; Z_br_; str_ß_; İst_nb_l |
| fn_regexp_match27 | `regexp_match` | same | ["a"]; ["a"]; ["ai"]; ["u"]; ["e"]; ["a"]; ["a"] | ["a"]; ["a"]; ["ai"]; ["u"]; ["e"]; ["a"]; ["a"] |
| fn_ascii28 | `ascii` | unsupported | 97; 66; 233; 95; 90; 115; 304 | ERROR: Postgres function ascii(text) is not supported |
| fn_chr29 | `chr` | unsupported | A; A; A; A; A; A; A | ERROR: Postgres function chr(bigint) is not supported |
| fn_md530 | `md5` | unsupported | 1f3870be274f6c49b3e31a0c6728957f; e6f9c347672daae5a2557ae118f44a1e; d63b831a8d3c3ff065b... | ERROR: Postgres function md5(text) is not supported |
| fn_sha25631 | `sha256` | same | \x3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b; \xf9782dd7999dc14b3... | \x3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b; \xf9782dd7999dc14b3... |
| fn_encode32 | `encode` | unsupported | 6170706c65; 42616e616e61; c3a9636c616972; 5f756e646572; 5a65627261; 73747261c39f65; c4b... | ERROR: Postgres function encode(bytea, text) is not supported |
| fn_format33 | `format` | unsupported | apple!; Banana!; éclair!; _under!; Zebra!; straße!; İstanbul! | ERROR: Postgres function format(text, text) is not supported |
| fn_quote_ident34 | `quote_ident` | differs | apple; "Banana"; "éclair"; _under; "Zebra"; "straße"; "İstanbul" | "apple"; "Banana"; "éclair"; "_under"; "Zebra"; "straße"; "İstanbul" |
| fn_to_hex35 | `to_hex` | unsupported | 1; 2; 3; 4; 5; 6; 7 | ERROR: Postgres function to_hex(bigint) is not supported |
| fn_starts_with36 | `starts_with` | same | true; false; false; false; false; false; false | true; false; false; false; false; false; false |
| fn_round37 | `round` | unsupported | 2; 2; 3; NULL; 0; -3; 10 | ERROR: Postgres function round(numeric) is not supported |
| fn_round38 | `round` | unsupported | 1.5; 2.3; 3; NULL; 0.1; -2.5; 10 | ERROR: function round(numeric, bigint) does not exist Hint: No function matches the giv... |
| fn_round39 | `round` | differs | 0; 0; 0; 0; NULL; 2; 0 | 0; 0; 0; 0; NULL; 3; 0 |
| fn_trunc40 | `trunc` | unsupported | 1; 2; 3; NULL; 0; -2; 10 | ERROR: Postgres function trunc(numeric) is not supported |
| fn_trunc41 | `trunc` | same | 0; 0; 0; 0; NULL; 2; 0 | 0; 0; 0; 0; NULL; 2; 0 |
| fn_ceil42 | `ceil` | same | 1; 1; 1; 1; NULL; 3; 0 | 1; 1; 1; 1; NULL; 3; 0 |
| fn_floor43 | `floor` | same | 1; 2; 3; NULL; 0; -3; 10 | 1; 2; 3; NULL; 0; -3; 10 |
| fn_abs44 | `abs` | same | 1.5; 2.25; 3; NULL; 0.1; 2.5; 10.005 | 1.5; 2.25; 3; NULL; 0.1; 2.5; 10.005 |
| fn_mod45 | `mod` | same | 1; 2; 0; 1; 2; 0; 1 | 1; 2; 0; 1; 2; 0; 1 |
| fn_power46 | `power` | pg_error | ERROR: value out of range: underflow (SQLSTATE 22003) | 0.010000000000000002; 0.04000000000000001; 0.09; 0; NULL; 6.25; 0 |
| fn_sqrt47 | `sqrt` | same | 0.31622776601683794; 0.4472135954999579; 0.5477225575051661; 1e-150; NULL; 1.5811388300... | 0.31622776601683794; 0.4472135954999579; 0.5477225575051661; 1e-150; NULL; 1.5811388300... |
| fn_exp48 | `exp` | same | 1.1051709180756477; 1.2214027581601699; 1.3498588075760032; 1; NULL; 12.182493960703473; 1 | 1.1051709180756477; 1.2214027581601699; 1.3498588075760032; 1; NULL; 12.182493960703473; 1 |
| fn_ln49 | `ln` | same | 0; 0.6931471805599453; 1.0986122886681098; 1.3862943611198906; 1.6094379124341003; 1.79... | 0; 0.6931471805599453; 1.0986122886681098; 1.3862943611198906; 1.6094379124341003; 1.79... |
| fn_log50 | `log` | same | 0; 0.3010299956639812; 0.47712125471966244; 0.6020599913279624; 0.6989700043360189; 0.7... | 0; 0.3010299956639812; 0.47712125471966244; 0.6020599913279624; 0.6989700043360189; 0.7... |
| fn_sign51 | `sign` | unsupported | 1; 1; 1; NULL; 1; -1; 1 | ERROR: Postgres function sign(numeric) is not supported |
| fn_sign52 | `sign` | same | 1; 1; 1; 1; NULL; 1; 0 | 1; 1; 1; 1; NULL; 1; 0 |
| fn_width_bucket53 | `width_bucket` | unsupported | 1; 1; 2; 1; NULL; 5; 1 | ERROR: function width_bucket(double precision, bigint, bigint, bigint) does not exist H... |
| fn_random54 | `random` | unsupported | true; true; true; true; true; true; true | ERROR: Postgres function random() is not supported |
| fn_greatest55 | `greatest` | differs | m; m; m; _under; Zebra; straße; m | m; m; éclair; m; m; straße; İstanbul |
| fn_least56 | `least` | unsupported | 1; 1; 1; 1; 0.1; -2.5; 1 | ERROR: Unsupported type PG.NUMERIC when calling pg.least() |
| fn_least57 | `least` | same | 0.1; 0.2; 0.3; 1e-300; 1; 1; 0 | 0.1; 0.2; 0.3; 1e-300; 1; 1; 0 |
| fn_greatest58 | `greatest` | same | 1; 1; 1; 1; 1; 2.5; 1 | 1; 1; 1; 1; 1; 2.5; 1 |
| fn_nullif59 | `nullif` | same | NULL; NULL; NULL; y; a; b; NULL | NULL; NULL; NULL; y; a; b; NULL |
| fn_coalesce60 | `coalesce` | same | apple; x; éclair; y; a; b; İstanbul | apple; x; éclair; y; a; b; İstanbul |
| fn_now61 | `now` | same | true; true; true; true; true; true; true | true; true; true; true; true; true; true |
| fn_date_trunc62 | `date_trunc` | differs | 2024-01-01T00:00:00.000000Z; 2024-03-10T00:00:00.000000Z; 2024-06-30T00:00:00.000000Z; ... | 2023-12-31T08:00:00.000000Z; 2024-03-10T08:00:00.000000Z; 2024-06-30T07:00:00.000000Z; ... |
| fn_date_trunc63 | `date_trunc` | same | 2024-01-01T00:00:00.000000Z; 2024-03-10T00:00:00.000000Z; 2024-06-30T00:00:00.000000Z; ... | 2024-01-01T00:00:00.000000Z; 2024-03-10T00:00:00.000000Z; 2024-06-30T00:00:00.000000Z; ... |
| fn_extract64 | `extract` | differs | 0; 9; 23; 16; 0; 12; 8 | 16; 1; 16; 8; 16; 5; 0 |
| fn_date_part65 | `date_part` | unsupported | 1; 0; 0; 2; 3; 4; 4 | ERROR: Postgres function date_part(text, timestamp with time zone) is not supported |
| fn_to_char66 | `to_char` | differs | 2024-01-01 00:00; 2024-03-10 09:30; 2024-06-30 23:59; 2024-12-31 16:00; 2025-01-01 00:0... | 2023-12-31 16:00; 2024-03-10 01:30; 2024-06-30 16:59; 2024-12-31 08:00; 2024-12-31 16:0... |
| fn_age67 | `age` | unsupported | interval(0mon,0d,0us); interval(0mon,0d,0us); interval(0mon,0d,0us); interval(0mon,0d,0... | ERROR: Postgres function age(timestamp with time zone, timestamp with time zone) is not... |
| fn_make_date68 | `make_date` | same | 2024-01-01T00:00:00.000000Z; 2024-01-01T00:00:00.000000Z; 2024-01-01T00:00:00.000000Z; ... | 2024-01-01T00:00:00.000000Z; 2024-01-01T00:00:00.000000Z; 2024-01-01T00:00:00.000000Z; ... |
| fn_to_timestamp69 | `to_timestamp` | same | 1970-01-01T00:00:00.000000Z; 1970-01-01T00:00:00.000000Z; 1970-01-01T00:00:00.000000Z; ... | 1970-01-01T00:00:00.000000Z; 1970-01-01T00:00:00.000000Z; 1970-01-01T00:00:00.000000Z; ... |
| fn_timezone70 | `timezone` | unsupported | 2024-01-01T00:00:00.123456Z; 2024-03-10T09:30:00.000000Z; 2024-06-30T23:59:59.999999Z; ... | ERROR: Postgres function timezone(text, timestamp with time zone) is not supported |
| fn_cast_ts_text71 | `cast_ts_text` | differs | 2024-01-01 00:00:00.123456+00; 2024-03-10 09:30:00+00; 2024-06-30 23:59:59.999999+00; 2... | 2023-12-31 16:00:00.123456-08; 2024-03-10 01:30:00-08; 2024-06-30 16:59:59.999999-07; 2... |
| fn_cast_ts_date72 | `cast_ts_date` | differs | 2024-01-01T00:00:00.000000Z; 2024-03-10T00:00:00.000000Z; 2024-06-30T00:00:00.000000Z; ... | 2023-12-31T00:00:00.000000Z; 2024-03-10T00:00:00.000000Z; 2024-06-30T00:00:00.000000Z; ... |
| fn_interval_add73 | `interval_add` | differs | 2024-01-02T00:00:00.123456Z; 2024-03-11T09:30:00.000000Z; 2024-07-01T23:59:59.999999Z; ... | 2024-01-02T00:00:00.123456Z; 2024-03-11T08:30:00.000000Z; 2024-07-01T23:59:59.999999Z; ... |
| fn_current_date74 | `current_date` | same | true; true; true; true; true; true; true | true; true; true; true; true; true; true |
| fn_jsonb_field75 | `jsonb_field` | same | [1, 2]; 2; NULL; NULL; NULL; NULL; NULL | [1, 2]; 2; NULL; NULL; NULL; NULL; NULL |
| fn_#>>76 | `#>>` | unsupported | 1; NULL; NULL; NULL; NULL; NULL; NULL | ERROR: Postgres function jsonb_extract_path_text(jsonb, text[]) is not supported |
| fn_jsonb_typeof77 | `jsonb_typeof` | same | object; object; NULL; array; object; object; object | object; object; NULL; array; object; object; object |
| fn_jsonb_build_object78 | `jsonb_build_object` | same | {"id":1}; {"id":2}; {"id":3}; {"id":4}; {"id":5}; {"id":6}; {"id":7} | {"id":1}; {"id":2}; {"id":3}; {"id":4}; {"id":5}; {"id":6}; {"id":7} |
| fn_json_build_object79 | `json_build_object` | unsupported | {"id":1}; {"id":2}; {"id":3}; {"id":4}; {"id":5}; {"id":6}; {"id":7} | ERROR: Unable to resolve argument type. Please consider adding an explicit cast |
| fn_to_jsonb80 | `to_jsonb` | same | apple; Banana; éclair; _under; Zebra; straße; İstanbul | apple; Banana; éclair; _under; Zebra; straße; İstanbul |
| fn_jsonb_array_length81 | `jsonb_array_length` | both_error | ERROR: cannot get array length of a scalar (SQLSTATE 22023) | ERROR: Postgres function jsonb_array_length(jsonb) is not supported |
| fn_jsonb_set82 | `jsonb_set` | both_error | ERROR: path element at position 1 is not an integer: "z" (SQLSTATE 22P02) | ERROR: path element at position 1 is not an integer: "z" |
| fn_jsonb_contains83 | `jsonb_contains` | same | false; true; NULL; false; false; false; false | false; true; NULL; false; false; false; false |
| fn_gen_random_uuid84 | `gen_random_uuid` | same | true; true; true; true; true; true; true | true; true; true; true; true; true; true |
| fn_array_length85 | `array_length` | same | 2; 2; 2; 2; 2; 2; 2 | 2; 2; 2; 2; 2; 2; 2 |
| fn_array_position86 | `array_position` | unsupported | 1; 2; 3; NULL; NULL; NULL; NULL | ERROR: Postgres function array_position(bigint[], bigint) is not supported |
| fn_array_to_string87 | `array_to_string` | same | apple; Banana,x; éclair; _under,y; Zebra,a; straße,b; İstanbul | apple; Banana,x; éclair; _under,y; Zebra,a; straße,b; İstanbul |
| fn_array_cat88 | `array_cat` | same | ["apple",null]; ["Banana","x"]; ["éclair",null]; ["_under","y"]; ["Zebra","a"]; ["straß... | ["apple",null]; ["Banana","x"]; ["éclair",null]; ["_under","y"]; ["Zebra","a"]; ["straß... |
| fn_cardinality89 | `cardinality` | unsupported | 1; 1; 1; 1; 1; 1; 1 | ERROR: Postgres function cardinality(bigint[]) is not supported |
| fn_generate_series90 | `generate_series` | unsupported | 1; 2; 1; 2; 1; 2; 1; 2; 1; 2; 1; 2; 1; 2 | ERROR: Set-returning functions are not allowed in SELECT |
| fn_unnest91 | `unnest` | unsupported | 1; 2; 1; 2; 1; 2; 1; 2; 1; 2; 1; 2; 1; 2 | ERROR: Set-returning functions are not allowed in SELECT |
| agg_sum92 | `sum` | same | 3.1 | 3.1 |
| agg_avg93 | `avg` | same | 0.5166666666666667 | 0.5166666666666667 |
| agg_avg94 | `avg` | same | 2.3925 | 2.3925 |
| agg_min95 | `min` | differs | apple | Banana |
| agg_max96 | `max` | differs | Zebra | İstanbul |
| agg_string_agg97 | `string_agg` | differs | apple,Banana,éclair,İstanbul,straße,_under,Zebra | Banana,Zebra,_under,apple,straße,éclair,İstanbul |
| agg_array_agg98 | `array_agg` | differs | ["apple","Banana","éclair","İstanbul","straße","_under","Zebra"] | ["Banana","Zebra","_under","apple","straße","éclair","İstanbul"] |
| agg_count99 | `count` | same | 4 | 4 |
| agg_stddev100 | `stddev` | unsupported | 0.9786044485218054 | ERROR: Postgres function stddev(double precision) is not supported |
| agg_variance101 | `variance` | unsupported | 17.6656375 | ERROR: Postgres function variance(numeric) is not supported |
| agg_bool_and102 | `bool_and` | same | false | false |
| agg_every103 | `every` | same | false | false |
| agg_percentile_cont104 | `percentile_cont` | unsupported | 0.15000000000000002 | ERROR: Postgres function percentile_cont(double precision) is not supported |
| agg_mode105 | `mode` | unsupported | a | ERROR: Postgres function mode(text) is not supported |
| agg_json_agg106 | `json_agg` | unsupported | ["apple","Banana","éclair","_under","Zebra","straße","İstanbul"] | ERROR: Postgres function json_agg(text) is not supported |
| agg_jsonb_agg107 | `jsonb_agg` | unsupported | ["apple","Banana","éclair","_under","Zebra","straße","İstanbul"] | ERROR: Postgres function jsonb_agg(text) is not supported |
| syn_order_text | `order_text` | differs | apple; Banana; éclair; İstanbul; straße; _under; Zebra | Banana; Zebra; _under; apple; straße; éclair; İstanbul |
| syn_order_text_desc | `order_text_desc` | differs | Zebra; _under; straße; İstanbul; éclair; Banana; apple | İstanbul; éclair; straße; apple; _under; Zebra; Banana |
| syn_order_nulls_default_asc | `order_nulls_default_asc` | same | 5\|a; 6\|b; 2\|x; 4\|y; 1\|NULL; 3\|NULL; 7\|NULL | 5\|a; 6\|b; 2\|x; 4\|y; 1\|NULL; 3\|NULL; 7\|NULL |
| syn_order_nulls_default_desc | `order_nulls_default_desc` | same | 1\|NULL; 3\|NULL; 7\|NULL; 4\|y; 2\|x; 6\|b; 5\|a | 1\|NULL; 3\|NULL; 7\|NULL; 4\|y; 2\|x; 6\|b; 5\|a |
| syn_order_nulls_first | `order_nulls_first` | same | 1\|NULL; 3\|NULL; 7\|NULL; 5\|a; 6\|b; 2\|x; 4\|y | 1\|NULL; 3\|NULL; 7\|NULL; 5\|a; 6\|b; 2\|x; 4\|y |
| syn_text_compare | `text_compare` | differs | 1; 2; 3; 4; 5; 6; 7 | 1; 3; 6; 7 |
| syn_distinct_on | `distinct_on` | unsupported | a\|5; b\|6; x\|2; y\|4; NULL\|1 | ERROR: Statements with DISTINCT ON clauses are not supported |
| syn_row_compare | `row_compare` | unsupported | 1; 2 | ERROR: Unsupported PostgreSQL expression type: RowCompareExpr |
| syn_collate | `collate` | unsupported | Banana; Zebra; _under; apple; straße; éclair; İstanbul | ERROR: Unsupported PostgreSQL expression type: CollateExpr |
| syn_any_array_literal | `any_array_literal` | unsupported | 1; 2 | ERROR: ANY, SOME, and IN expressions with array literal arguments are not supported. Tr... |
| syn_any_array_ctor | `any_array_ctor` | same | 1; 2 | 1; 2 |
| syn_in_list | `in_list` | same | 1; 2 | 1; 2 |
| syn_ilike | `ilike` | same | 1 | 1 |
| syn_regex_match | `regex_match` | same | 1; 6 | 1; 6 |
| syn_similar_to | `similar_to` | unsupported | 1; 6 | ERROR: Postgres function similar_to_escape(text) is not supported |
| syn_window_row_number | `window_row_number` | unsupported | 1\|1; 2\|2; 3\|3; 4\|4; 5\|5; 6\|6; 7\|7 | ERROR: Statements with WINDOW clauses are not supported |
| syn_window_sum | `window_sum` | unsupported | 1\|0.4; 2\|0.2; 3\|0.4; 4\|1e-300; 5\|NULL; 6\|2.5; 7\|0.4 | ERROR: Statements with WINDOW clauses are not supported |
| syn_recursive_cte | `recursive_cte` | unsupported | 1; 2; 3 | ERROR: Recursive WITH expressions are not supported |
| syn_cte | `cte` | same | 7 | 7 |
| syn_lateral | `lateral` | unsupported | 1\|2; 2\|4; 3\|6; 4\|8; 5\|10; 6\|12; 7\|14 | ERROR: Lateral subqueries are not supported |
| syn_full_join | `full_join` | same | 1; 2; 3; 4; 5; 6; 7 | 1; 2; 3; 4; 5; 6; 7 |
| syn_fetch_first | `fetch_first` | same | 1; 2 | 1; 2 |
| syn_tablesample | `tablesample` | same | true | true |
| syn_for_update_autocommit | `for_update_autocommit` | unsupported | 1 | ERROR: FOR UPDATE is not supported in this transaction type. |
| syn_skip_locked | `skip_locked` | unsupported | 1 | ERROR: Statements with locking clauses with lock wait policies are not supported |
| syn_group_by_rollup | `group_by_rollup` | unsupported | NULL\|7; NULL\|3; a\|1; b\|1; y\|1; x\|1 | ERROR: Statements with GROUPING SET clauses are not supported |
| syn_having | `having` | same | NULL\|3 | NULL\|3 |
| syn_exists_subquery | `exists_subquery` | same | 1; 2; 3; 4; 5; 6 | 1; 2; 3; 4; 5; 6 |
| syn_union | `union` | same | 1; 2; 3; 4; 5; 6; 7; 11; 12; 13; 14; 15; 16; 17 | 1; 2; 3; 4; 5; 6; 7; 11; 12; 13; 14; 15; 16; 17 |
| syn_int_division | `int_division` | same | 1\|0\|0\|1; 2\|1\|-1\|2; 3\|1\|-1\|0; 4\|2\|-2\|1; 5\|2\|-2\|2; 6\|3\|-3\|0; 7\|3\|-3\|1 | 1\|0\|0\|1; 2\|1\|-1\|2; 3\|1\|-1\|0; 4\|2\|-2\|1; 5\|2\|-2\|2; 6\|3\|-3\|0; 7\|3\|-3\|1 |
| syn_float_division_zero | `float_division_zero` | both_error | ERROR: division by zero (SQLSTATE 22012) | ERROR: division by zero: 1 / 0 |
| syn_numeric_division | `numeric_division` | same | 0.5; 0.75; 1; NULL; 0.03333333333333333333; -0.83333333333333333333; 3.335 | 0.5; 0.75; 1; NULL; 0.03333333333333333333; -0.83333333333333333333; 3.335 |
| syn_float_text | `float_text` | same | 0.1; 0.2; 0.3; 1e-300; NULL; 2.5; 0 | 0.1; 0.2; 0.3; 1e-300; NULL; 2.5; 0 |
| syn_numeric_text | `numeric_text` | same | 1.5; 2.25; 3; NULL; 0.1; -2.5; 10.005 | 1.5; 2.25; 3; NULL; 0.1; -2.5; 10.005 |
| syn_bool_text | `bool_text` | same | true; true; true; true; NULL; true; false | true; true; true; true; NULL; true; false |
| syn_jsonb_text | `jsonb_text` | same | {"a": [1, 2], "b": 1}; {"a": 2}; NULL; []; {"k": "v"}; {"n": null}; {"x": 1.0} | {"a": [1, 2], "b": 1}; {"a": 2}; NULL; []; {"k": "v"}; {"n": null}; {"x": 1.0} |
| syn_timestamp_without_tz | `timestamp_without_tz` | unsupported | 2024-01-01T00:00:00.123456Z; 2024-03-10T09:30:00.000000Z; 2024-06-30T23:59:59.999999Z; ... | ERROR: The Postgres Type is not supported: timestamp without time zone |
| syn_int4_overflow | `int4_overflow` | both_error | ERROR: integer out of range (SQLSTATE 22003) | ERROR: cannot cast type bigint to integer |
| syn_show_timezone | `show_timezone` | differs | Etc/UTC | UTC |
