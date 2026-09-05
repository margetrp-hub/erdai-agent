package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const unifiedRuntimeMigrationID = "runtime-database-v1"

var unifiedRuntimeTables = []string{
	"agent_runs",
	"agent_deliveries",
	"platform_reply_routes",
	"platform_sent_deliveries",
	"platform_sent_delivery_parts",
	"agent_affiliate_bindings",
	"agent_affiliate_owners",
	"agent_points_ledger",
	"agent_points_catalog",
	"agent_search_runs",
	"agent_search_queries",
	"agent_memories",
	"conversation_events",
	"conversation_state",
	"relationship_events",
	"member_relationship_state",
	"media_quota_config",
	"media_quota_usage",
}

func sameDatabasePath(left, right string) bool {
	left, leftErr := filepath.Abs(filepath.Clean(left))
	right, rightErr := filepath.Abs(filepath.Clean(right))
	return leftErr == nil && rightErr == nil && left == right
}

func readOnlySQLiteURI(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(absolute),
		RawQuery: "mode=ro",
	}).String(), nil
}

type sqliteTableColumn struct {
	name         string
	notNull      bool
	defaultValue sql.NullString
	primaryKey   bool
}

func sqliteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sqliteTableColumns(ctx context.Context, tx *sql.Tx, schema, table string) ([]sqliteTableColumn, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA "+sqliteIdentifier(schema)+".table_info("+sqliteIdentifier(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make([]sqliteTableColumn, 0)
	for rows.Next() {
		var column sqliteTableColumn
		var cid, notNull, primaryKey int
		var kind string
		if err = rows.Scan(&cid, &column.name, &kind, &notNull, &column.defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		column.notNull = notNull == 1
		column.primaryKey = primaryKey == 1
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func legacyColumnExpression(table, column string, sourceColumns map[string]bool) (string, bool) {
	source := "legacy_source." + sqliteIdentifier(column)
	if table == "agent_runs" {
		switch column {
		case "transport":
			if sourceColumns[column] {
				return "COALESCE(NULLIF(trim(CAST(" + source + " AS TEXT)), ''), 'qq_official')", true
			}
			return "'qq_official'", true
		case "sender_ref":
			if sourceColumns[column] {
				return "COALESCE(NULLIF(trim(CAST(" + source + " AS TEXT)), ''), 'legacy:unknown')", true
			}
			return "'legacy:unknown'", true
		case "is_admin":
			if sourceColumns[column] {
				return "COALESCE(" + source + ", 0)", true
			}
			return "0", true
		}
	}
	if sourceColumns[column] {
		return source, true
	}
	return "", false
}

func legacyTableCopyStatement(ctx context.Context, tx *sql.Tx, table string) (string, error) {
	source, err := sqliteTableColumns(ctx, tx, "legacy_runtime", table)
	if err != nil {
		return "", fmt.Errorf("inspect legacy %s columns: %w", table, err)
	}
	target, err := sqliteTableColumns(ctx, tx, "main", table)
	if err != nil {
		return "", fmt.Errorf("inspect unified %s columns: %w", table, err)
	}
	sourceNames := make(map[string]bool, len(source))
	for _, column := range source {
		sourceNames[column.name] = true
	}
	targetNames := make([]string, 0, len(target))
	expressions := make([]string, 0, len(target))
	for _, column := range target {
		expression, supplied := legacyColumnExpression(table, column.name, sourceNames)
		if !supplied {
			if column.notNull && !column.defaultValue.Valid && !column.primaryKey {
				return "", fmt.Errorf("legacy %s is missing required column %s", table, column.name)
			}
			continue
		}
		targetNames = append(targetNames, sqliteIdentifier(column.name))
		expressions = append(expressions, expression)
	}
	if len(targetNames) == 0 {
		return "", fmt.Errorf("legacy %s has no compatible columns", table)
	}
	return "INSERT INTO main." + sqliteIdentifier(table) + " (" + strings.Join(targetNames, ", ") + ") SELECT " +
		strings.Join(expressions, ", ") + " FROM legacy_runtime." + sqliteIdentifier(table) + " AS legacy_source", nil
}

func mergeLegacyRuntimeDatabase(
	ctx context.Context,
	db *sql.DB,
	unifiedPath string,
	legacyPath string,
) error {
	legacyPath = filepath.Clean(legacyPath)
	if db == nil || legacyPath == "." || sameDatabasePath(unifiedPath, legacyPath) {
		return nil
	}
	if _, err := os.Stat(legacyPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect legacy runtime database: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS runtime_database_migrations (
			id TEXT PRIMARY KEY,
			migrated_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create runtime migration marker: %w", err)
	}
	var marker int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM runtime_database_migrations WHERE id = ?",
		unifiedRuntimeMigrationID,
	).Scan(&marker); err != nil {
		return err
	}
	if marker == 1 {
		return nil
	}

	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve unified database connection: %w", err)
	}
	defer connection.Close()
	legacyURI, err := readOnlySQLiteURI(legacyPath)
	if err != nil {
		return fmt.Errorf("resolve legacy runtime database: %w", err)
	}
	if _, err = connection.ExecContext(ctx, "ATTACH DATABASE ? AS legacy_runtime", legacyURI); err != nil {
		return fmt.Errorf("attach legacy runtime database: %w", err)
	}
	defer connection.ExecContext(context.Background(), "DETACH DATABASE legacy_runtime")

	var integrity string
	if err = connection.QueryRowContext(ctx, "PRAGMA legacy_runtime.quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("legacy runtime integrity check failed: %s: %w", integrity, err)
	}
	sourceTables := map[string]bool{}
	rows, err := connection.QueryContext(ctx,
		"SELECT name FROM legacy_runtime.sqlite_master WHERE type = 'table'",
	)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		sourceTables[name] = true
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if !sourceTables["agent_runs"] || !sourceTables["agent_deliveries"] {
		return errors.New("legacy runtime database is missing required run tables")
	}

	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	sourceCounts := map[string]int{}
	for _, table := range unifiedRuntimeTables {
		if !sourceTables[table] {
			continue
		}
		var sourceCount, targetCount int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM legacy_runtime."+table).Scan(&sourceCount); err != nil {
			return fmt.Errorf("count legacy %s: %w", table, err)
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM main."+table).Scan(&targetCount); err != nil {
			return fmt.Errorf("count unified %s: %w", table, err)
		}
		if table == "media_quota_config" {
			if _, err = tx.ExecContext(ctx, "DELETE FROM main."+table); err != nil {
				return err
			}
		} else if targetCount != 0 {
			return fmt.Errorf("unified runtime table %s is not empty", table)
		}
		copyStatement, statementErr := legacyTableCopyStatement(ctx, tx, table)
		if statementErr != nil {
			return statementErr
		}
		if _, err = tx.ExecContext(ctx, copyStatement); err != nil {
			return fmt.Errorf("copy legacy runtime table %s: %w", table, err)
		}
		sourceCounts[table] = sourceCount
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE main.agent_runs
		SET state = 'queued', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE state = 'running' AND input_cipher IS NOT NULL
	`); err != nil {
		return fmt.Errorf("recover imported runtime runs: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE main.media_quota_usage
		SET reserved_count = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE reserved_count > 0
	`); err != nil {
		return fmt.Errorf("release imported media reservations: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO runtime_database_migrations (id, migrated_at)
		VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
	`, unifiedRuntimeMigrationID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit unified runtime migration: %w", err)
	}
	committed = true

	for table, expected := range sourceCounts {
		var actual int
		if err = connection.QueryRowContext(ctx, "SELECT count(*) FROM main."+table).Scan(&actual); err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("unified runtime table %s has %d rows, want %d", table, actual, expected)
		}
	}
	if err = connection.QueryRowContext(ctx, "PRAGMA main.quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("unified database integrity check failed: %s: %w", integrity, err)
	}
	return nil
}
