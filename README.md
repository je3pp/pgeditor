# pgeditor

A small, keyboard-driven terminal UI (TUI) for browsing and editing PostgreSQL tables. It is written in Go with [pgx](https://github.com/jackc/pgx), [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lip Gloss](https://github.com/charmbracelet/lipgloss), and it uses the same connection settings as `psql`.

Current version: **0.1**

## Features

- Connects like `psql`: `PG*` environment variables, `~/.pgpass`, or a connection string.
- Table overview with row counts, per-table size, and total database size.
- Cursor-driven grid with horizontal and vertical scrolling.
- Edit any cell, set a cell to `NULL`, add rows, and delete rows (with confirmation).
- Paging (100 rows per page) with correct ordering by primary key.
- Case-insensitive search across all columns, executed in the database (covers all pages).
- Works with any column type by exchanging values as text (numbers, dates, UUIDs, booleans and so on).

## Requirements

- PostgreSQL (developed against PostgreSQL 18; any supported version should work).
- A recent Go toolchain to build (developed on FreeBSD; it builds wherever Go and a terminal are available).

## Build

```sh
git clone <your-repository-url> pgeditor
cd pgeditor
go mod tidy
go build -o pgeditor
```

Set a version at build time:

```sh
go build -ldflags "-X main.version=0.2" -o pgeditor 
./pgeditor --version
```

## Usage

Using `psql`-style environment variables and `~/.pgpass`:

```sh
PGHOST=localhost PGDATABASE=appdb PGUSER=appuser ./pgeditor
```

Using a connection string:

```sh
./pgeditor "host=localhost port=5432 dbname=appdb user=appuser"
./pgeditor "postgres://appuser@localhost:5432/appdb?sslmode=require"
```

If no password is given, the underlying driver reads `~/.pgpass` (mode `0600`) just as `psql` does. For a local Unix socket with `trust` authentication, no password is needed at all.

## Key bindings

### Table list

| Key | Action |
|---|---|
| Up / Down, `k` / `j` | Move selection |
| Enter | Open table |
| `r` | Refresh row counts and sizes |
| `q` | Quit |

### Table view

| Key | Action |
|---|---|
| Arrow keys, `h` `j` `k` `l` | Move the cell cursor |
| PgUp / PgDn | Move one screen of rows |
| `>` or `.` | Next page |
| `<` or `,` | Previous page |
| Enter or `e` | Edit the selected cell |
| `a` | Add a row |
| `d` | Delete the selected row (asks for confirmation) |
| `/` | Search all columns |
| `r` | Reload the current page |
| Esc | Clear the active search; if none, return to the table list |
| `q` | Quit |

### Edit, add, search and delete dialogs

| Dialog | Keys |
|---|---|
| Edit cell | Enter saves, Ctrl+N saves `NULL`, Esc cancels |
| Add row | Tab / Down next field, Shift+Tab / Up previous field, Ctrl+S or Enter on the last field inserts, Esc cancels |
| Search | Enter applies (empty clears the filter), Esc cancels |
| Delete | `y` confirms, `n` or Esc cancels |

`Ctrl+C` quits from any screen.

## Behavior notes

- **Ordering.** Rows are ordered by the primary key using the real column types, so integer keys sort numerically. Tables without a primary key are ordered by `ctid`.
- **Editing and deleting** require a primary key, because rows are identified by it. Tables without one can be viewed and can receive inserts, but cannot be edited or deleted from.
- **Generated columns.** `GENERATED ALWAYS` columns are read-only and are skipped in the add-row form.
- **Add-row form.** Empty fields are omitted from the `INSERT`, so the column default (or `NULL`) applies. An empty string therefore cannot be inserted through the form.
- **Search** is a substring match (`ILIKE '%text%'`) against every column cast to text, including columns that are scrolled out of view. Short numeric terms can match unexpected columns, for example fractional seconds in timestamps. The `%` and `_` characters are matched literally.
- **Row counts.** Tables with fewer than 100,000 estimated rows get an exact `count(*)`. Larger tables show a planner estimate prefixed with `~`. A table that cannot be read shows `?`.
- **Sizes** include indexes and TOAST data.
- **Types.** Values are displayed as text and written back as text parameters, and PostgreSQL converts them to the column type. Invalid input is reported in the status line.
- **SQL safety.** Schema, table and column names are quoted with pgx identifier quoting, and all values are sent as bound parameters.

## Security notes

- Edits and deletes run immediately against the connected database (each statement is auto-committed). Use a role with only the privileges you need, and consider working on a copy of production data.
- `pg_hba.conf` rules with `trust` authentication allow connections without a password. For anything beyond a local sandbox, use `scram-sha-256`.
- `~/.pgpass` stores passwords in plain text. Keep it at mode `0600`.

## Limitations

- One statement per action; there are no transactions or undo.
- Primary-key matching compares the key as text (`column::text = $n`), which cannot use the primary-key index. This is fine for small and medium tables but slow on very large ones.
- Offset-based paging gets slower deep into very large tables.
- Wide characters are measured by rune count, so some alignment glitches may appear with East Asian or emoji text.
- Binary (`bytea`) and very large values are shown as text and truncated in the grid.

## Project layout

| File | Purpose |
|---|---|
| `main.go` | Bubble Tea model, key handling and views |
| `database.go` | Connection, table overview, paging, search and write operations |
| `LICENSE` | BSD 2-Clause license |

## Dependencies

- [github.com/jackc/pgx/v5](https://github.com/jackc/pgx)
- [github.com/charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea)
- [github.com/charmbracelet/bubbles](https://github.com/charmbracelet/bubbles) (text input)
- [github.com/charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss)

Each dependency has its own license; see the respective projects.

## License

BSD 2-Clause "Simplified" License. See [LICENSE](LICENSE).

Copyright (c) 2026 Javor.Evstatiev@3pplus.at 
