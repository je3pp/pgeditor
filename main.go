package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5"
)

//go:embed VERSION
var versionFile string

var version = strings.TrimSpace(versionFile)

const pageSize = 100

type tableRef struct {
	Schema string
	Name   string
	Rows   int64  // -1 = unknown
	Approx bool   // true = planner estimate
	Size   string // total size incl. indexes
}

type colInfo struct {
	Name     string
	Auto     bool
	ReadOnly bool
}

type screen int

const (
	screenTables screen = iota
	screenRows
	screenEdit
	screenInsert
	screenConfirm
	screenSearch
)

type model struct {
	conn       *pgx.Conn
	connString string
	screen     screen
	width      int
	height     int
	status     string
	err        error

	dbName string
	dbSize string

	tables      []tableRef
	tableCursor int

	cur    tableRef
	cols   []colInfo
	pk     []string
	rows   [][]*string
	rowCur int
	colCur int
	rowOff int
	colOff int
	page   int
	total  int
	search string

	input       textinput.Model
	searchInput textinput.Model
	form        []textinput.Model
	formCols    []int
	formIdx     int
}

type connectedMsg struct {
	Conn   *pgx.Conn
	Tables []tableRef
	DBName string
	DBSize string
	Err    error
}

type tableLoadedMsg struct {
	Cols  []colInfo
	PK    []string
	Rows  [][]*string
	Page  int
	Total int
	Err   error
}

type opDoneMsg struct {
	Info string
	Err  error
}

var (
	appStyle      = lipgloss.NewStyle().Padding(1, 2)
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("117"))
	cellSelStyle  = lipgloss.NewStyle().Reverse(true).Bold(true)
	curRowStyle   = lipgloss.NewStyle().Bold(true)
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("pgedit", version)
		return
	}

	connString := ""
	if len(os.Args) > 1 {
		connString = os.Args[1]
	}

	ti := textinput.New()
	ti.CharLimit = 1000
	ti.Width = 60

	si := textinput.New()
	si.CharLimit = 200
	si.Width = 60
	si.Placeholder = "text to find in any column"

	m := model{connString: connString, screen: screenTables, input: ti, searchInput: si}

	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func (m model) Init() tea.Cmd {
	return connect(m.connString)
}

func (m model) reload() tea.Cmd {
	return loadTable(m.conn, m.cur, m.page, m.search)
}

func (m model) totalPages() int {
	if m.total == 0 {
		return 1
	}
	return (m.total + pageSize - 1) / pageSize
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.fixScroll()
		return m, nil

	case connectedMsg:
		if msg.Err != nil {
			if m.conn == nil {
				m.err = msg.Err
				return m, tea.Quit
			}
			m.status = "Refresh failed: " + msg.Err.Error()
			return m, nil
		}
		m.conn = msg.Conn
		m.tables = msg.Tables
		m.dbName, m.dbSize = msg.DBName, msg.DBSize
		m.tableCursor = clamp(m.tableCursor, 0, maxInt(0, len(m.tables)-1))
		if len(m.tables) == 0 {
			m.status = "No accessible user tables."
		}
		return m, nil

	case tableLoadedMsg:
		if msg.Err != nil {
			m.status = "Error: " + msg.Err.Error()
			if m.screen != screenRows {
				m.screen = screenTables
			}
			return m, nil
		}
		m.cols, m.pk, m.rows = msg.Cols, msg.PK, msg.Rows
		m.page, m.total = msg.Page, msg.Total
		m.screen = screenRows
		m.rowCur = clamp(m.rowCur, 0, maxInt(0, len(m.rows)-1))
		m.colCur = clamp(m.colCur, 0, maxInt(0, len(m.cols)-1))
		m.fixScroll()
		return m, nil

	case opDoneMsg:
		m.screen = screenRows
		if msg.Err != nil {
			m.status = "Failed: " + msg.Err.Error()
			return m, nil
		}
		m.status = msg.Info
		return m, m.reload()

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}

	switch m.screen {
	case screenTables:
		return updateTables(m, msg)
	case screenRows:
		return updateRows(m, msg)
	case screenEdit:
		return updateEdit(m, msg)
	case screenInsert:
		return updateInsert(m, msg)
	case screenConfirm:
		return updateConfirm(m, msg)
	case screenSearch:
		return updateSearch(m, msg)
	}
	return m, nil
}

func updateTables(m model, msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.tableCursor = clamp(m.tableCursor-1, 0, maxInt(0, len(m.tables)-1))
	case "down", "j":
		m.tableCursor = clamp(m.tableCursor+1, 0, maxInt(0, len(m.tables)-1))
	case "r":
		m.status = ""
		return m, refreshTables(m.conn)
	case "enter":
		if len(m.tables) == 0 {
			return m, nil
		}
		m.cur = m.tables[m.tableCursor]
		m.rowCur, m.colCur, m.rowOff, m.colOff = 0, 0, 0, 0
		m.page, m.search = 0, ""
		m.status = ""
		return m, m.reload()
	}
	return m, nil
}

func updateRows(m model, msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	m.status = ""

	switch k.String() {
	case "q":
		return m, tea.Quit

	case "esc":
		if m.search != "" {
			m.search, m.page = "", 0
			m.rowCur, m.rowOff = 0, 0
			return m, m.reload()
		}
		m.screen = screenTables
		return m, refreshTables(m.conn)

	case "up", "k":
		m.rowCur = clamp(m.rowCur-1, 0, maxInt(0, len(m.rows)-1))
	case "down", "j":
		m.rowCur = clamp(m.rowCur+1, 0, maxInt(0, len(m.rows)-1))
	case "pgup":
		m.rowCur = clamp(m.rowCur-m.visibleRows(), 0, maxInt(0, len(m.rows)-1))
	case "pgdown":
		m.rowCur = clamp(m.rowCur+m.visibleRows(), 0, maxInt(0, len(m.rows)-1))
	case "left", "h":
		m.colCur = clamp(m.colCur-1, 0, maxInt(0, len(m.cols)-1))
	case "right", "l":
		m.colCur = clamp(m.colCur+1, 0, maxInt(0, len(m.cols)-1))

	case ">", ".":
		if m.page+1 >= m.totalPages() {
			m.status = "Already on the last page."
			return m, nil
		}
		m.page++
		m.rowCur, m.rowOff = 0, 0
		return m, m.reload()

	case "<", ",":
		if m.page == 0 {
			m.status = "Already on the first page."
			return m, nil
		}
		m.page--
		m.rowCur, m.rowOff = 0, 0
		return m, m.reload()

	case "/":
		m.searchInput.SetValue(m.search)
		m.searchInput.CursorEnd()
		cmd := m.searchInput.Focus()
		m.screen = screenSearch
		return m, cmd

	case "r":
		return m, m.reload()

	case "enter", "e":
		if len(m.rows) == 0 {
			m.status = "No rows to edit. Press a to add one."
			return m, nil
		}
		if len(m.pk) == 0 {
			m.status = "Table has no primary key: editing is disabled."
			return m, nil
		}
		if m.cols[m.colCur].ReadOnly {
			m.status = "Column is GENERATED ALWAYS and cannot be edited."
			return m, nil
		}
		val := ""
		if p := m.rows[m.rowCur][m.colCur]; p != nil {
			val = *p
		}
		m.input.SetValue(val)
		m.input.CursorEnd()
		cmd := m.input.Focus()
		m.screen = screenEdit
		return m, cmd

	case "a":
		m.form, m.formCols, m.formIdx = nil, nil, 0
		for i, c := range m.cols {
			if c.ReadOnly {
				continue
			}
			ti := textinput.New()
			ti.Width = 50
			ti.CharLimit = 1000
			if c.Auto {
				ti.Placeholder = "(default)"
			} else {
				ti.Placeholder = "(empty = NULL)"
			}
			m.form = append(m.form, ti)
			m.formCols = append(m.formCols, i)
		}
		var cmd tea.Cmd
		if len(m.form) > 0 {
			cmd = m.form[0].Focus()
		}
		m.screen = screenInsert
		return m, cmd

	case "d":
		if len(m.rows) == 0 {
			return m, nil
		}
		if len(m.pk) == 0 {
			m.status = "Table has no primary key: deleting is disabled."
			return m, nil
		}
		m.screen = screenConfirm
		return m, nil
	}

	m.fixScroll()
	return m, nil
}

func updateSearch(m model, msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			m.searchInput.Blur()
			m.screen = screenRows
			return m, nil
		case "enter":
			m.search = strings.TrimSpace(m.searchInput.Value())
			m.searchInput.Blur()
			m.page = 0
			m.rowCur, m.rowOff = 0, 0
			return m, m.reload()
		}
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	return m, cmd
}

func updateEdit(m model, msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			m.input.Blur()
			m.screen = screenRows
			return m, nil
		case "enter":
			v := m.input.Value()
			m.input.Blur()
			return m, updateCell(m.conn, m.cur, m.cols, m.pk, m.rows[m.rowCur], m.colCur, &v)
		case "ctrl+n":
			m.input.Blur()
			return m, updateCell(m.conn, m.cur, m.cols, m.pk, m.rows[m.rowCur], m.colCur, nil)
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m model) submitInsert() (tea.Model, tea.Cmd) {
	var names, values []string
	for i, ci := range m.formCols {
		if v := m.form[i].Value(); v != "" {
			names = append(names, m.cols[ci].Name)
			values = append(values, v)
		}
	}
	return m, insertRow(m.conn, m.cur, names, values)
}

func (m model) moveForm(delta int) (tea.Model, tea.Cmd) {
	if len(m.form) == 0 {
		return m, nil
	}
	m.form[m.formIdx].Blur()
	m.formIdx = clamp(m.formIdx+delta, 0, len(m.form)-1)
	return m, m.form[m.formIdx].Focus()
}

func updateInsert(m model, msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			m.screen = screenRows
			return m, nil
		case "ctrl+s":
			return m.submitInsert()
		case "enter":
			if len(m.form) == 0 || m.formIdx == len(m.form)-1 {
				return m.submitInsert()
			}
			return m.moveForm(1)
		case "tab", "down":
			return m.moveForm(1)
		case "shift+tab", "up":
			return m.moveForm(-1)
		}
	}
	if len(m.form) == 0 {
		return m, nil
	}
	var cmd tea.Cmd
	m.form[m.formIdx], cmd = m.form[m.formIdx].Update(msg)
	return m, cmd
}

func updateConfirm(m model, msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "y", "Y":
			return m, deleteRow(m.conn, m.cur, m.cols, m.pk, m.rows[m.rowCur])
		case "n", "N", "esc":
			m.screen = screenRows
		}
	}
	return m, nil
}

// ---------- layout helpers ----------

func (m model) visibleRows() int {
	return maxInt(3, m.height-12)
}

func (m model) availWidth() int {
	return maxInt(20, m.width-8)
}

func (m model) colWidths() []int {
	widths := make([]int, len(m.cols))
	for i, c := range m.cols {
		widths[i] = len([]rune(c.Name)) + 1
	}
	for _, row := range m.rows {
		for i, p := range row {
			if l := len([]rune(cellText(p))); l > widths[i] {
				widths[i] = l
			}
		}
	}
	for i := range widths {
		widths[i] = clamp(widths[i], 3, 30)
	}
	return widths
}

func visibleCols(widths []int, off, avail int) []int {
	var vis []int
	used := 0
	for i := off; i < len(widths); i++ {
		need := widths[i]
		if len(vis) > 0 {
			need += 3
		}
		if len(vis) > 0 && used+need > avail {
			break
		}
		used += need
		vis = append(vis, i)
	}
	return vis
}

func (m *model) fixScroll() {
	vr := m.visibleRows()
	if m.rowCur < m.rowOff {
		m.rowOff = m.rowCur
	}
	if m.rowCur >= m.rowOff+vr {
		m.rowOff = m.rowCur - vr + 1
	}
	if m.rowOff < 0 {
		m.rowOff = 0
	}

	if len(m.cols) == 0 {
		return
	}
	if m.colCur < m.colOff {
		m.colOff = m.colCur
	}
	widths := m.colWidths()
	for m.colOff < m.colCur {
		vis := visibleCols(widths, m.colOff, m.availWidth())
		if len(vis) > 0 && m.colCur <= vis[len(vis)-1] {
			break
		}
		m.colOff++
	}
}

// ---------- views ----------

func (m model) View() string {
	if m.err != nil {
		return appStyle.Render(errorStyle.Render("Connection error:\n\n" + m.err.Error()))
	}

	switch m.screen {
	case screenTables:
		return appStyle.Render(m.viewTables())
	case screenRows:
		return appStyle.Render(m.viewRows())
	case screenEdit:
		return appStyle.Render(m.viewEdit())
	case screenInsert:
		return appStyle.Render(m.viewInsert())
	case screenConfirm:
		return appStyle.Render(m.viewConfirm())
	case screenSearch:
		return appStyle.Render(m.viewSearch())
	}
	return ""
}

func (m model) viewTables() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("PostgreSQL Table Editor v"+version) + "\n")
	if m.dbName != "" {
		b.WriteString(helpStyle.Render(fmt.Sprintf(
			"database %s • %s • %d tables", m.dbName, m.dbSize, len(m.tables))) + "\n")
	}
	b.WriteString("\n")

	nameW, rowsW := len("TABLE"), len("ROWS")
	names := make([]string, len(m.tables))
	counts := make([]string, len(m.tables))
	for i, t := range m.tables {
		names[i] = t.Schema + "." + t.Name
		counts[i] = formatCount(t)
		nameW = maxInt(nameW, len([]rune(names[i])))
		rowsW = maxInt(rowsW, len(counts[i]))
	}

	b.WriteString(helpStyle.Render(fmt.Sprintf("  %-*s  %*s  %s", nameW, "TABLE", rowsW, "ROWS", "SIZE")) + "\n")

	for i := range m.tables {
		line := fmt.Sprintf("%-*s  %*s  %s", nameW, names[i], rowsW, counts[i], m.tables[i].Size)
		if i == m.tableCursor {
			b.WriteString(selectedStyle.Render("▶ "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}

	if m.status != "" {
		b.WriteString("\n" + statusStyle.Render(m.status) + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("↑/↓ move • Enter open • r refresh • q quit"))
	b.WriteString("\n" + helpStyle.Render("~ = estimate (tables ≥ 100,000 rows) • SIZE includes indexes and TOAST"))
	return b.String()
}

func (m model) viewRows() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Table "+m.cur.Schema+"."+m.cur.Name) + "\n")

	info := fmt.Sprintf("page %d/%d • %d rows", m.page+1, m.totalPages(), m.total)
	if m.search != "" {
		info += fmt.Sprintf(" • filter: %q (Esc clears)", m.search)
	}
	b.WriteString(helpStyle.Render(info) + "\n\n")

	if len(m.cols) == 0 {
		return b.String()
	}

	widths := m.colWidths()
	vis := visibleCols(widths, m.colOff, m.availWidth())

	headers := make([]string, len(vis))
	for k, ci := range vis {
		name := m.cols[ci].Name
		if contains(m.pk, name) {
			name += "*"
		}
		headers[k] = fit(name, widths[ci])
	}
	b.WriteString("  " + headerStyle.Render(strings.Join(headers, " │ ")) + "\n")

	end := minInt(len(m.rows), m.rowOff+m.visibleRows())
	for i := m.rowOff; i < end; i++ {
		gutter := "  "
		if i == m.rowCur {
			gutter = "▶ "
		}

		cells := make([]string, len(vis))
		for k, ci := range vis {
			text := fit(cellText(m.rows[i][ci]), widths[ci])
			switch {
			case i == m.rowCur && ci == m.colCur:
				cells[k] = cellSelStyle.Render(text)
			case i == m.rowCur:
				cells[k] = curRowStyle.Render(text)
			default:
				cells[k] = text
			}
		}
		b.WriteString(gutter + strings.Join(cells, " │ ") + "\n")
	}

	if len(m.rows) == 0 {
		b.WriteString("  (no rows)\n")
	}

	pos := fmt.Sprintf("column %q", m.cols[m.colCur].Name)
	if len(m.rows) > 0 {
		pos = fmt.Sprintf("row %d of %d • %s", m.page*pageSize+m.rowCur+1, m.total, pos)
	}
	b.WriteString("\n" + helpStyle.Render(pos) + "\n")
	b.WriteString(statusStyle.Render(m.status) + "\n")
	b.WriteString(helpStyle.Render("←↑↓→ move • Enter edit • a add • d delete • / search • < > page • r refresh • Esc back • q quit"))
	return b.String()
}

func (m model) viewSearch() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Search "+m.cur.Schema+"."+m.cur.Name) + "\n\n")
	b.WriteString("Case-insensitive substring match across all columns.\n\n")
	b.WriteString(m.searchInput.View() + "\n\n")
	b.WriteString(helpStyle.Render("Enter search (empty = clear) • Esc cancel"))
	return b.String()
}

func (m model) viewEdit() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Edit "+m.cur.Schema+"."+m.cur.Name) + "\n\n")
	b.WriteString(fmt.Sprintf("Row:    %d\n", m.page*pageSize+m.rowCur+1))
	b.WriteString(fmt.Sprintf("Column: %s\n\n", m.cols[m.colCur].Name))
	b.WriteString(m.input.View() + "\n\n")
	b.WriteString(helpStyle.Render("Enter save • Ctrl+N set NULL • Esc cancel"))
	return b.String()
}

func (m model) viewInsert() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Add row to "+m.cur.Schema+"."+m.cur.Name) + "\n\n")

	if len(m.form) == 0 {
		b.WriteString("All columns are generated; a row with default values will be inserted.\n")
	}
	for i, ci := range m.formCols {
		label := fmt.Sprintf("%-22s", m.cols[ci].Name)
		if i == m.formIdx {
			label = selectedStyle.Render("▶ " + label)
		} else {
			label = "  " + label
		}
		b.WriteString(label + " " + m.form[i].View() + "\n")
	}

	b.WriteString("\n" + helpStyle.Render("Tab/↓ next • Shift+Tab/↑ previous • Enter on last field or Ctrl+S insert • Esc cancel"))
	b.WriteString("\n" + helpStyle.Render("Empty fields are omitted, so the column default (or NULL) applies."))
	return b.String()
}

func (m model) viewConfirm() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Delete row?") + "\n\n")
	for i, c := range m.cols {
		b.WriteString(fmt.Sprintf("%-22s %s\n", c.Name, cellText(m.rows[m.rowCur][i])))
	}
	b.WriteString("\n" + errorStyle.Render("Delete this row permanently? (y/n)"))
	return b.String()
}

// ---------- small helpers ----------

func formatCount(t tableRef) string {
	if t.Rows < 0 {
		return "?"
	}
	s := groupInt(t.Rows)
	if t.Approx {
		return "~" + s
	}
	return s
}

func groupInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func cellText(p *string) string {
	if p == nil {
		return "NULL"
	}
	return *p
}

func fit(s string, w int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", "⏎"), "\t", " ")
	r := []rune(s)
	if len(r) > w {
		if w > 1 {
			r = append(r[:w-1], '…')
		} else {
			r = r[:w]
		}
	}
	return string(r) + strings.Repeat(" ", w-len(r))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
