
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jackc/pgx/v5"
)

const approxThreshold = 100000

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func quote(s string) string { return pgx.Identifier{s}.Sanitize() }

func qualified(ref tableRef) string {
	return pgx.Identifier{ref.Schema, ref.Name}.Sanitize()
}

func whereClause(pk []string, first int) string {
	parts := make([]string, len(pk))
	for i, c := range pk {
		parts[i] = fmt.Sprintf("%s::text = $%d", quote(c), first+i)
	}
	return strings.Join(parts, " AND ")
}

func pkArgs(cols []colInfo, pk []string, row []*string) []any {
	args := make([]any, 0, len(pk))
	for _, p := range pk {
		for i, c := range cols {
			if c.Name == p {
				args = append(args, row[i])
			}
		}
	}
	return args
}

// ---------- connection and table overview ----------

func connect(connString string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		conn, err := pgx.Connect(ctx, connString)
		if err != nil {
			return connectedMsg{Err: err}
		}

		tables, name, size, err := fetchOverview(ctx, conn)
		if err != nil {
			conn.Close(ctx)
			return connectedMsg{Err: err}
		}
		return connectedMsg{Conn: conn, Tables: tables, DBName: name, DBSize: size}
	}
}

func refreshTables(conn *pgx.Conn) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		tables, name, size, err := fetchOverview(ctx, conn)
		if err != nil {
			return connectedMsg{Err: err}
		}
		return connectedMsg{Conn: conn, Tables: tables, DBName: name, DBSize: size}
	}
}

func fetchOverview(ctx context.Context, conn *pgx.Conn) ([]tableRef, string, string, error) {
	var dbName, dbSize string
	err := conn.QueryRow(ctx, `
		SELECT current_database(), pg_size_pretty(pg_database_size(current_database()))
	`).Scan(&dbName, &dbSize)
	if err != nil {
		return nil, "", "", err
	}

	rows, err := conn.Query(ctx, `
		SELECT t.table_schema, t.table_name,
		       c.reltuples::bigint,
		       pg_size_pretty(pg_total_relation_size(c.oid))
		FROM information_schema.tables t
		JOIN pg_namespace n ON n.nspname = t.table_schema
		JOIN pg_class c ON c.relnamespace = n.oid AND c.relname = t.table_name
		WHERE t.table_type = 'BASE TABLE'
		  AND t.table_schema NOT IN ('pg_catalog', 'information_schema')
		ORDER BY t.table_schema, t.table_name
	`)
	if err != nil {
		return nil, "", "", err
	}

	var tables []tableRef
	var estimates []int64
	for rows.Next() {
		var t tableRef
		var est int64
		if err := rows.Scan(&t.Schema, &t.Name, &est, &t.Size); err != nil {
			rows.Close()
			return nil, "", "", err
		}
		tables = append(tables, t)
		estimates = append(estimates, est)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", "", err
	}

	for i := range tables {
		if estimates[i] >= approxThreshold {
			tables[i].Rows = estimates[i]
			tables[i].Approx = true
			continue
		}
		var n int64
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+qualified(tables[i])).Scan(&n); err != nil {
			tables[i].Rows = -1
		} else {
			tables[i].Rows = n
		}
	}

	return tables, dbName, dbSize, nil
}

// ---------- table data ----------

func loadTable(conn *pgx.Conn, ref tableRef, page int, search string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		colRows, err := conn.Query(ctx, `
			SELECT column_name,
			       (column_default IS NOT NULL
			        OR is_identity = 'YES'
			        OR is_generated = 'ALWAYS') AS auto,
			       (is_generated = 'ALWAYS'
			        OR COALESCE(identity_generation, '') = 'ALWAYS') AS readonly
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2
			ORDER BY ordinal_position
		`, ref.Schema, ref.Name)
		if err != nil {
			return tableLoadedMsg{Err: err}
		}

		var cols []colInfo
		for colRows.Next() {
			var c colInfo
			if err := colRows.Scan(&c.Name, &c.Auto, &c.ReadOnly); err != nil {
				colRows.Close()
				return tableLoadedMsg{Err: err}
			}
			cols = append(cols, c)
		}
		colRows.Close()
		if err := colRows.Err(); err != nil {
			return tableLoadedMsg{Err: err}
		}

		pkRows, err := conn.Query(ctx, `
			SELECT kcu.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
			  ON tc.constraint_name = kcu.constraint_name
			 AND tc.constraint_schema = kcu.constraint_schema
			WHERE tc.table_schema = $1
			  AND tc.table_name = $2
			  AND tc.constraint_type = 'PRIMARY KEY'
			ORDER BY kcu.ordinal_position
		`, ref.Schema, ref.Name)
		if err != nil {
			return tableLoadedMsg{Err: err}
		}

		var pk []string
		for pkRows.Next() {
			var name string
			if err := pkRows.Scan(&name); err != nil {
				pkRows.Close()
				return tableLoadedMsg{Err: err}
			}
			pk = append(pk, name)
		}
		pkRows.Close()
		if err := pkRows.Err(); err != nil {
			return tableLoadedMsg{Err: err}
		}

		// Optional search filter across all columns.
		where := ""
		var args []any
		if search != "" && len(cols) > 0 {
			parts := make([]string, len(cols))
			for i, c := range cols {
				parts[i] = fmt.Sprintf("t.%s::text ILIKE $1", quote(c.Name))
			}
			where = " WHERE " + strings.Join(parts, " OR ")
			args = append(args, "%"+likeEscaper.Replace(search)+"%")
		}

		var total int64
		countQuery := fmt.Sprintf("SELECT count(*) FROM %s AS t%s", qualified(ref), where)
		if err := conn.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
			return tableLoadedMsg{Err: err}
		}

		lastPage := 0
		if total > 0 {
			lastPage = int((total - 1) / pageSize)
		}
		if page > lastPage {
			page = lastPage
		}
		if page < 0 {
			page = 0
		}

		// Select as text, but ORDER BY the real table columns via alias t.
		// A bare ORDER BY "id" would bind to the text output column "id".
		selectList := make([]string, len(cols))
		for i, c := range cols {
			selectList[i] = "t." + quote(c.Name) + "::text"
		}

		order := "t.ctid"
		if len(pk) > 0 {
			orderList := make([]string, len(pk))
			for i, c := range pk {
				orderList[i] = "t." + quote(c)
			}
			order = strings.Join(orderList, ", ")
		}

		query := fmt.Sprintf(
			"SELECT %s FROM %s AS t%s ORDER BY %s LIMIT %d OFFSET %d",
			strings.Join(selectList, ", "),
			qualified(ref),
			where,
			order,
			pageSize,
			page*pageSize,
		)

		dataRows, err := conn.Query(ctx, query, args...)
		if err != nil {
			return tableLoadedMsg{Err: err}
		}
		defer dataRows.Close()

		var result [][]*string
		for dataRows.Next() {
			values := make([]*string, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := dataRows.Scan(ptrs...); err != nil {
				return tableLoadedMsg{Err: err}
			}
			result = append(result, values)
		}
		if err := dataRows.Err(); err != nil {
			return tableLoadedMsg{Err: err}
		}

		return tableLoadedMsg{
			Cols:  cols,
			PK:    pk,
			Rows:  result,
			Page:  page,
			Total: int(total),
		}
	}
}

func updateCell(
	conn *pgx.Conn,
	ref tableRef,
	cols []colInfo,
	pk []string,
	row []*string,
	colIdx int,
	value *string,
) tea.Cmd {
	return func() tea.Msg {
		query := fmt.Sprintf(
			"UPDATE %s SET %s = $1 WHERE %s",
			qualified(ref),
			quote(cols[colIdx].Name),
			whereClause(pk, 2),
		)

		args := append([]any{value}, pkArgs(cols, pk, row)...)

		tag, err := conn.Exec(context.Background(), query, args...)
		if err != nil {
			return opDoneMsg{Err: err}
		}
		if tag.RowsAffected() != 1 {
			return opDoneMsg{Err: fmt.Errorf("expected 1 row updated, got %d", tag.RowsAffected())}
		}
		return opDoneMsg{Info: "Cell updated."}
	}
}

func insertRow(conn *pgx.Conn, ref tableRef, names, values []string) tea.Cmd {
	return func() tea.Msg {
		var query string
		var args []any

		if len(names) == 0 {
			query = fmt.Sprintf("INSERT INTO %s DEFAULT VALUES", qualified(ref))
		} else {
			quoted := make([]string, len(names))
			params := make([]string, len(names))
			args = make([]any, len(names))
			for i, n := range names {
				quoted[i] = quote(n)
				params[i] = fmt.Sprintf("$%d", i+1)
				args[i] = values[i]
			}
			query = fmt.Sprintf(
				"INSERT INTO %s (%s) VALUES (%s)",
				qualified(ref),
				strings.Join(quoted, ", "),
				strings.Join(params, ", "),
			)
		}

		if _, err := conn.Exec(context.Background(), query, args...); err != nil {
			return opDoneMsg{Err: err}
		}
		return opDoneMsg{Info: "Row inserted."}
	}
}

func deleteRow(conn *pgx.Conn, ref tableRef, cols []colInfo, pk []string, row []*string) tea.Cmd {
	return func() tea.Msg {
		query := fmt.Sprintf("DELETE FROM %s WHERE %s", qualified(ref), whereClause(pk, 1))

		tag, err := conn.Exec(context.Background(), query, pkArgs(cols, pk, row)...)
		if err != nil {
			return opDoneMsg{Err: err}
		}
		if tag.RowsAffected() != 1 {
			return opDoneMsg{Err: fmt.Errorf("expected 1 row deleted, got %d", tag.RowsAffected())}
		}
		return opDoneMsg{Info: "Row deleted."}
	}
}
