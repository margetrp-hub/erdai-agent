package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

const historicalCoreTimestamp = "2026-01-02T03:04:05.123Z"
const pollutedCoreTimestamp = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"

func TestCoreConfigSchemaInitialTimestampValues(t *testing.T) {
	store, err := openCoreConfigStore(filepath.Join(t.TempDir(), "core.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertCoreSchemaTimestampValues(t, store.db)
}

func TestCoreConfigSchemaUpgradeTimestampValues(t *testing.T) {
	type target struct{ table, predicate string }
	profile := target{"persona_runtime_profiles", "persona_id = 'doubao'"}
	template := target{"agent_policy_templates", "id = 'doubao-default'"}
	instance := target{"agent_instances", "id = 'doubao-qq'"}
	runtime := target{"runtime_config", "id = 1"}
	group := target{"integration_settings", "id = 'group_chat_policy'"}
	affiliate := target{"integration_settings", "id = 'affiliate_policy'"}
	plugin := target{"agent_plugins", "id = 'affiliate-invite'"}
	for _, test := range []struct {
		name    string
		version int
		setup   string
		targets []target
	}{
		{
			name: "plugin-contracts-v72", version: 71,
			targets: []target{{"agent_plugins", "id = 'knowledge-retrieval'"}},
		},
		{
			name: "affiliate-alias-v73", version: 72,
			setup:   `UPDATE integration_settings SET config_json = json_set(config_json, '$.pointsAliases', json('["/查询积分"]')) WHERE id = 'affiliate_policy'`,
			targets: []target{affiliate, plugin},
		},
		{
			name: "rewards-v78", version: 77,
			setup:   `UPDATE integration_settings SET config_json = '{}' WHERE id = 'affiliate_policy'`,
			targets: []target{affiliate, plugin},
		},
		{
			name: "doubao-appearance-v79", version: 78,
			setup: `UPDATE personas SET visual_description = '可使用米白色交领上衣配浅青色滚边' WHERE id = 'doubao';
				UPDATE appearance_libraries SET visual_description = '可使用米白色交领上衣配浅青色滚边' WHERE id = 'persona-appearance-doubao'`,
			targets: []target{{"personas", "id = 'doubao'"}, {"appearance_libraries", "id = 'persona-appearance-doubao'"}},
		},
		{
			name: "xiaoman-appearance-v80", version: 79,
			setup: `UPDATE personas SET visual_description = '服装随时间与话题自然变化', source_version = 'go-schema-74' WHERE id = 'xiaoman';
				UPDATE appearance_libraries SET visual_description = '服装随时间与话题自然变化' WHERE id = 'persona-appearance-xiaoman'`,
			targets: []target{{"personas", "id = 'xiaoman'"}, {"appearance_libraries", "id = 'persona-appearance-xiaoman'"}},
		},
		{
			name: "provider-timeout-v81", version: 80,
			setup: `INSERT INTO provider_connections (id, provider, api_base, timeout_seconds, created_at, updated_at)
				VALUES ('connection-grok-paid-chat', 'timestamp-test', 'https://example.invalid/v1', 10, '2026-01-02T03:04:05.123Z', '2026-01-02T03:04:05.123Z')
				ON CONFLICT(id) DO UPDATE SET timeout_seconds = 10`,
			targets: []target{{"provider_connections", "id = 'connection-grok-paid-chat'"}},
		},
		{
			name: "radar-source-v82", version: 81,
			setup:   `UPDATE integration_settings SET config_json = json_set(config_json, '$.radarUrl', 'https://codexradar.com/api/intelligence-efficiency-metrics') WHERE id = 'ops_policy'`,
			targets: []target{{"integration_settings", "id = 'ops_policy'"}},
		},
		{
			name: "participation-v86", version: 85,
			setup: `INSERT INTO persona_runtime_profiles (persona_id, profile_json, updated_at)
				VALUES ('doubao', '{"participationMode":"addressed_only","unaddressedMode":"off","proactiveEnabled":true}', '2026-01-02T03:04:05.123Z')
				ON CONFLICT(persona_id) DO UPDATE SET profile_json = excluded.profile_json;
				UPDATE agent_policy_templates SET name = '豆包默认策略', description = '由旧角色运行档案迁移', config_json = '{"participationMode":"addressed_only"}' WHERE id = 'doubao-default';
				UPDATE agent_instances SET overrides_json = '{"participationMode":"social"}' WHERE id = 'doubao-qq'`,
			targets: []target{profile, template},
		},
		{
			name: "humanization-v87", version: 85,
			setup: `INSERT INTO persona_runtime_profiles (persona_id, profile_json, updated_at)
				VALUES ('doubao', '{"participationMode":"social","maxReplyChars":30,"expressionPrompt":"甜妹式撒娇"}', '2026-01-02T03:04:05.123Z')
				ON CONFLICT(persona_id) DO UPDATE SET profile_json = excluded.profile_json;
				UPDATE agent_policy_templates SET config_json = '{"participationMode":"social","maxReplyChars":30,"expressionPrompt":"甜妹式撒娇"}' WHERE id = 'doubao-default';
				UPDATE agent_instances SET overrides_json = '{"participationMode":"social","expressionPrompt":"群里没被明确叫到"}' WHERE id = 'doubao-qq'`,
			targets: []target{profile, template, instance},
		},
		{
			name: "prompt-softening-v88", version: 85,
			setup: `UPDATE runtime_config SET reply_style = '普通聊天默认一句，必要时最多两句；先压缩表达，再按完整句组织，不硬截断、不留残句、不复读。' WHERE id = 1;
				UPDATE integration_settings SET config_json = json_set(config_json, '$.replyExtraPrompt', '这是自然群聊，不是客服答复。默认一句，必要时最多两句；说完整，不复述、不总结、不列点。') WHERE id = 'group_chat_policy'`,
			targets: []target{runtime, group},
		},
		{
			name: "prompt-softening-v89", version: 85,
			setup: `UPDATE runtime_config SET reply_style = '普通聊天默认一句，通常八到三十五个汉字；确实需要补充时最多两句，总字数尽量不超过四十五个汉字。' WHERE id = 1;
				UPDATE integration_settings SET config_json = json_set(config_json, '$.replyExtraPrompt', '顺着当前群聊自然接话。完整优先，闲聊可以短；别复述，也别为了显得能干而展开。') WHERE id = 'group_chat_policy'`,
			targets: []target{runtime, group},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "core.sqlite3")
			store, err := openCoreConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if test.setup != "" {
				if _, err = store.db.Exec(test.setup); err != nil {
					t.Fatal(err)
				}
			}
			for _, row := range test.targets {
				// A real content migration may replace a previously polluted value,
				// but only with its actual migration time, never a guessed old date.
				result, err := store.db.Exec("UPDATE "+row.table+" SET updated_at = ? WHERE "+row.predicate, pollutedCoreTimestamp)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := result.RowsAffected(); err != nil || count != 1 {
					t.Fatalf("setup %s: updated %d rows: %v", row.table, count, err)
				}
			}
			if _, err = store.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", test.version)); err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			started := time.Now().UTC()
			store, err = openCoreConfigStore(path)
			finished := time.Now().UTC()
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range test.targets {
				var raw string
				if err := store.db.QueryRow("SELECT updated_at FROM " + row.table + " WHERE " + row.predicate).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				updated, err := time.Parse(time.RFC3339Nano, raw)
				if err != nil {
					t.Fatalf("%s.updated_at = %q: %v", row.table, raw, err)
				}
				// Persisted timestamps lose Go's monotonic clock component. The
				// Docker VM can adjust wall time during this call; allow a small
				// skew (also covering SQL's millisecond precision), not old dates.
				const clockTolerance = 50 * time.Millisecond
				if updated.Before(started.Add(-clockTolerance)) || updated.After(finished.Add(clockTolerance)) {
					t.Errorf("%s.updated_at = %s, outside migration interval [%s, %s] with %s clock tolerance", row.table, raw, started, finished, clockTolerance)
				}
			}
			assertCoreSchemaTimestampValues(t, store.db)
		})
	}
}

func TestCoreConfigSchemaReopenPreservesHistoricalTimestampValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for id, timestamp := range map[string]string{
		"knowledge-retrieval": historicalCoreTimestamp,
		"document-multimodal": pollutedCoreTimestamp,
	} {
		if _, err := store.db.Exec("UPDATE agent_plugins SET updated_at = ? WHERE id = ?", timestamp, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, expected := range map[string]string{
		"knowledge-retrieval": historicalCoreTimestamp,
		"document-multimodal": pollutedCoreTimestamp,
	} {
		var actual string
		if err := store.db.QueryRow("SELECT updated_at FROM agent_plugins WHERE id = ?", id).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			t.Errorf("unchanged plugin %s timestamp = %q, want preserved %q", id, actual, expected)
		}
	}
}

func assertCoreSchemaTimestampValues(t *testing.T, db *sql.DB) {
	t.Helper()
	// Inspect both SQL-expression seeds and parameterized migrations. Collect
	// columns before querying values because the store uses one connection.
	rows, err := db.Query(`SELECT s.name, p.name FROM sqlite_master s, pragma_table_info(s.name) p
		WHERE s.type = 'table' AND p.name IN ('created_at', 'updated_at') ORDER BY s.name, p.name`)
	if err != nil {
		t.Fatal(err)
	}
	var columns [][2]string
	for rows.Next() {
		var column [2]string
		if err := rows.Scan(&column[0], &column[1]); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	checked := 0
	for _, column := range columns {
		rows, err := db.Query(fmt.Sprintf(`SELECT %s FROM %s WHERE %s IS NOT NULL AND %s != ''`, column[1], column[0], column[1], column[1]))
		if err != nil {
			t.Fatal(err)
		}
		invalid := 0
		var firstInvalid string
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				invalid++
				if firstInvalid == "" {
					firstInvalid = value
				}
			}
			checked++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if invalid > 0 {
			t.Errorf("%s.%s contains %d invalid timestamps, first = %q", column[0], column[1], invalid, firstInvalid)
		}
	}
	if checked == 0 {
		t.Fatal("no seeded timestamps were checked")
	}
}
