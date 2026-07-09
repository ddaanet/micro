package display

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	runewidth "github.com/mattn/go-runewidth"
	"github.com/micro-editor/micro/v2/internal/buffer"
	"github.com/micro-editor/micro/v2/internal/config"
	ulua "github.com/micro-editor/micro/v2/internal/lua"
	"github.com/micro-editor/micro/v2/internal/screen"
	tcell "github.com/micro-editor/tcell/v2"
	"github.com/stretchr/testify/assert"
	lua "github.com/yuin/gopher-lua"
)

const updateGoldenUsage = "rewrite the -- want -- section of golden files"

var update = flag.Bool("update", false, updateGoldenUsage)

// sim is the simulation screen shared by every test in this package. It is
// a fixed 40x12: large enough for the default window plus a margin, small
// enough to keep goldens compact.
var sim tcell.SimulationScreen

// screenW, screenH are the fixed simulation screen dimensions. A scenario
// window must fit within them with a one-cell margin (see scenario.render).
const (
	screenW = 40
	screenH = 12
)

func init() {
	ulua.L = lua.NewState()
	config.InitRuntimeFiles(false)
	config.InitGlobalSettings()

	// Install fixed style keys so the style layer is deterministic and
	// independent of the default colorscheme, which doesn't define them.
	// Colorscheme is nil until InitColorscheme runs (which needs a real
	// colorscheme file on disk), so this harness allocates the map itself.
	//
	// bufwindow.go applies "color-column" and "cursor-line" by decomposing
	// the configured style and using its *foreground* as the highlight
	// background (fg, _, _ := s.Decompose(); style = style.Background(fg)),
	// matching how a real colorscheme file's bare color name for these keys
	// parses to a foreground-only style. Installing these with
	// .Background(...) instead would decompose to a default foreground and
	// render as a no-op highlight, so these use .Foreground(...).
	config.Colorscheme = make(map[string]tcell.Style)
	config.Colorscheme["color-column"] =
		tcell.StyleDefault.Foreground(tcell.ColorRed)
	config.Colorscheme["cursor-line"] =
		tcell.StyleDefault.Foreground(tcell.ColorBlue)

	var err error
	sim, err = screen.InitSimScreen(screenW, screenH)
	if err != nil {
		panic(err)
	}
}

// scenario is a render described by one golden file. See parseScenario for
// the file format.
type scenario struct {
	settings map[string]interface{}

	winX, winY int
	winW, winH int

	cursorX, cursorY int
	startCol         int

	withStyle bool
	text      string
}

// parseScenario reads a golden scenario file. The format is a directive
// header, the buffer text, and the expected serialized screen, split by two
// marker lines:
//
//	# free-text comment lines describing the scenario's rationale (optional)
//	win 36x10@2,1
//	set softwrap true
//	set wordwrap false
//	cursor 0,1
//	startcol 5
//	style
//	-- text --
//	<buffer content>
//	-- want --
//	<expected serialized screen>
//
// Everything before "-- text --" is directives; blank lines and lines
// beginning with '#' are ignored. The directives are:
//
//   - win WxH@X,Y   window geometry; required, so every scenario file is
//     self-describing rather than depending on a harness-invented default.
//   - set NAME VAL  a buffer setting. VAL is "true"/"false" -> bool,
//     else a float64 if it parses as one, else a string (micro's Settings
//     map convention).
//   - cursor X,Y    cursor position; optional, default 0,0.
//   - startcol N    horizontal scroll offset; optional, default 0.
//   - style         emit the style layer in the output; optional.
//
// The text section is the buffer content with exactly one trailing newline
// stripped, so an empty section is an empty buffer. The first "-- want --"
// marks the boundary. The want section is the expected serialization (see
// serializeScreen), regenerated in place by -update.
//
// parseScenario returns the scenario, the want section, and the raw prefix
// up to and including the "-- want --" line, which -update preserves
// byte-for-byte while rewriting only the want body.
func parseScenario(t *testing.T, raw string) (sc scenario, want, prefix string) {
	t.Helper()

	sc = scenario{
		settings: map[string]interface{}{},
	}

	textAt := lineIndex(raw, "-- text --")
	if textAt < 0 {
		t.Fatalf("scenario missing -- text -- marker")
	}
	afterText := textAt + len("-- text --\n")
	rel := lineIndex(raw[afterText:], "-- want --")
	if rel < 0 {
		t.Fatalf("scenario missing -- want -- marker")
	}
	wantAt := afterText + rel
	afterWant := wantAt + len("-- want --\n")

	sawWin := false
	for _, line := range strings.Split(raw[:textAt], "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "win":
			if _, err := fmt.Sscanf(fields[1], "%dx%d@%d,%d",
				&sc.winW, &sc.winH, &sc.winX, &sc.winY); err != nil {
				t.Fatalf("bad win directive %q: %v", line, err)
			}
			sawWin = true
		case "set":
			sc.settings[fields[1]] = parseSettingValue(
				strings.Join(fields[2:], " "))
		case "cursor":
			if _, err := fmt.Sscanf(fields[1], "%d,%d",
				&sc.cursorX, &sc.cursorY); err != nil {
				t.Fatalf("bad cursor directive %q: %v", line, err)
			}
		case "startcol":
			n, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatalf("bad startcol directive %q: %v", line, err)
			}
			sc.startCol = n
		case "style":
			sc.withStyle = true
		default:
			t.Fatalf("unknown directive %q", fields[0])
		}
	}

	if !sawWin {
		t.Fatalf("missing required win directive")
	}

	sc.text = strings.TrimSuffix(raw[afterText:wantAt], "\n")
	return sc, raw[afterWant:], raw[:afterWant]
}

// lineIndex returns the byte offset at which marker appears as a whole line
// in s, or -1 if it does not.
func lineIndex(s, marker string) int {
	switch {
	case s == marker, strings.HasPrefix(s, marker+"\n"):
		return 0
	case strings.HasSuffix(s, "\n"+marker):
		return len(s) - len(marker)
	}
	if i := strings.Index(s, "\n"+marker+"\n"); i >= 0 {
		return i + 1
	}
	return -1
}

// parseSettingValue converts a setting's textual value to the type micro's
// Settings map stores: bool for true/false, float64 for anything numeric,
// otherwise the string as-is.
func parseSettingValue(s string) interface{} {
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// render builds the buffer and window described by sc, displays it onto the
// shared sim screen, and returns the serialized screen.
func (sc scenario) render(t *testing.T) string {
	t.Helper()

	if sc.winX+sc.winW+1 > screenW || sc.winY+sc.winH+1 > screenH {
		t.Fatalf("window %dx%d@%d,%d does not fit in the "+
			"%dx%d screen with a one-cell margin",
			sc.winW, sc.winH, sc.winX, sc.winY, screenW, screenH)
	}

	// Clear the front buffer so stale content from a previous scenario can't
	// bleed through into cells this render leaves untouched.
	screen.Screen.Clear()
	screen.Screen.Sync()

	buf := buffer.NewBufferFromString(sc.text, "", buffer.BTDefault)
	for k, v := range sc.settings {
		buf.Settings[k] = v
	}

	w := NewBufWindow(sc.winX, sc.winY, sc.winW, sc.winH, buf)
	w.StartCol = sc.startCol
	buf.GetActiveCursor().GotoLoc(buffer.Loc{X: sc.cursorX, Y: sc.cursorY})

	w.Display()

	return serializeScreen(t, sim, sc.withStyle)
}

// serializeScreen renders the current contents of s as golden text:
//
//	 <tens-digit column ruler>
//	 <units-digit column ruler>
//	|<row 0>|
//	|<row 1>|
//	...
//
// and, when withStyle is true, a second grid of the same shape showing
// only deviations from the default style: a cell in the default style is
// blank, and each distinct non-default style, in row-major
// first-appearance order, is assigned a letter, followed by a legend
// decomposing each lettered style.
//
// Rows are joined with "\n" only (never "\r\n"); goldens must be
// regenerated with -update on a system that preserves LF endings (see
// .gitattributes).
func serializeScreen(t *testing.T, s tcell.SimulationScreen, withStyle bool) string {
	t.Helper()

	s.Show()
	cells, w, h := s.GetContents()

	lines := rulerLines(w)
	for y := 0; y < h; y++ {
		lines = append(lines, borderedRuneRow(t, cells, w, y))
	}

	if withStyle {
		lines = append(lines, "")
		lines = append(lines, styleLayerLines(cells, w, h)...)
	}

	return strings.Join(lines, "\n") + "\n"
}

// rulerLines builds the two-row column ruler (tens digits, then units
// digits) spanning width w, padded by one space on each side to line up
// with the '|' border columns of the rune-grid rows.
func rulerLines(w int) []string {
	tens := make([]byte, w)
	units := make([]byte, w)
	for x := 0; x < w; x++ {
		tens[x] = byte('0' + (x/10)%10)
		units[x] = byte('0' + x%10)
	}
	return []string{
		" " + string(tens) + " ",
		" " + string(units) + " ",
	}
}

// borderedRuneRow renders screen row y as a single line: the row's runes
// (space for any blank cell) framed by a '|' border on each side. A cell
// counts as blank if it has no rune content at all, or if micro's own
// renderer wrote an explicit space there -- which is what actually happens
// in this harness: bufwindow.go fills untouched columns, and the shadow
// column trailing a wide rune, by writing a literal ' ' rather than
// leaving the cell untouched.
//
// A double-width rune (e.g. a CJK ideograph) occupies two screen cells: its
// own cell plus that following shadow cell. This grid emits the glyph once,
// for its own cell, and omits the shadow cell from the rune grid entirely,
// so the row renders at true screen width in any CJK-capable font instead
// of gaining a spurious extra blank column per wide glyph. One consequence:
// a wide glyph spans two ruler columns, so in a row containing wide runes,
// a character's index in the rendered string no longer equals its screen
// cell index -- unlike the style grid, which always emits exactly one
// character per screen cell, including shadow cells, so its indices stay
// cell-aligned throughout. Before skipping a shadow cell, borderedRuneRow
// asserts that it really is blank, using the same test as for any other
// blank cell; if some other, non-blank rune has been written there, that
// cell is not a shadow at all but real content this serializer would
// otherwise silently discard, so the test fails loudly instead.
func borderedRuneRow(t *testing.T, cells []tcell.SimCell, w, y int) string {
	t.Helper()

	isEmpty := func(c tcell.SimCell) bool {
		return len(c.Runes) == 0 || c.Runes[0] == 0 || c.Runes[0] == ' '
	}

	var b strings.Builder
	b.WriteByte('|')
	for x := 0; x < w; x++ {
		c := cells[y*w+x]
		if isEmpty(c) {
			b.WriteRune(' ')
			continue
		}
		r := c.Runes[0]
		b.WriteRune(r)
		if runewidth.RuneWidth(r) == 2 && x+1 < w {
			if shadow := cells[y*w+x+1]; !isEmpty(shadow) {
				t.Fatalf("screen cell (%d,%d) overlaps the wide rune at (%d,%d)",
					x+1, y, x, y)
			}
			x++
		}
	}
	b.WriteByte('|')
	return b.String()
}

// styleLayerLines builds the opt-in style grid plus a legend. The grid
// shows only deviations from the default style: a cell in the default
// style (config.DefStyle, which is also the style of a cleared cell) is
// blank, and every distinct non-default style is assigned a letter in
// row-major first-appearance order, bordered the same way as the rune
// grid. The legend decomposes only the lettered (deviating) styles.
// Marking only deviations, the way Vim, Neovim, and ratatui do, keeps the
// grid legible: blank means "default here" and a letter pinpoints exactly
// where a highlight lands.
func styleLayerLines(cells []tcell.SimCell, w, h int) []string {
	letterOf := map[tcell.Style]byte{}
	var order []tcell.Style
	letterFor := func(st tcell.Style) byte {
		if st == config.DefStyle {
			return ' '
		}
		if l, ok := letterOf[st]; ok {
			return l
		}
		l := byte('A' + len(order))
		letterOf[st] = l
		order = append(order, st)
		return l
	}

	grid := make([]string, h)
	for y := 0; y < h; y++ {
		var b strings.Builder
		b.WriteByte('|')
		for x := 0; x < w; x++ {
			b.WriteByte(letterFor(cells[y*w+x].Style))
		}
		b.WriteByte('|')
		grid[y] = b.String()
	}

	lines := append([]string{"# styles"}, grid...)
	lines = append(lines, "")
	for i, st := range order {
		fg, bg, attr := st.Decompose()
		lines = append(lines, fmt.Sprintf("%c: fg=%s bg=%s attrs=%#x",
			byte('A'+i), colorName(fg), colorName(bg), attr))
	}
	return lines
}

// colorName formats a color as "#rrggbb", or "default" for the unset color
// (whose Hex() is negative).
func colorName(c tcell.Color) string {
	if h := c.Hex(); h >= 0 {
		return fmt.Sprintf("#%06x", h)
	}
	return "default"
}

// TestRender renders every scenario file under testdata/<group>/ and
// compares (or rewrites, under -update) its -- want -- section. The subtest
// name is <group>/<name>.
func TestRender(t *testing.T) {
	files, err := filepath.Glob("testdata/*/*.golden")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	for _, path := range files {
		path := path
		name := strings.TrimSuffix(
			strings.TrimPrefix(path, "testdata/"), ".golden")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sc, want, prefix := parseScenario(t, string(raw))
			got := sc.render(t)

			if *update {
				if err := os.WriteFile(
					path, []byte(prefix+got), 0644); err != nil {
					t.Fatal(err)
				}
				return
			}
			assert.Equal(t, want, got)
		})
	}
}

// TestRenderDeadZoneStale proves that the softwrapcolumn dead-zone fill
// actively blanks stale cells rather than relying on an already-blank
// screen. A single render can't show this: a freshly cleared screen is
// blank in every cell, so the dead zone reads blank whether or not the
// fill loop runs. To force the two cases apart, this renders buffer A --
// which wraps at the full pane width, so its rows carry real, non-blank
// content across the columns that become buffer B's dead zone -- then
// swaps in buffer B (softwrapcolumn=20) and renders again onto the same
// screen with no intervening Clear. Correct code re-blanks B's dead zone
// over buffer A's stale content; code that skips the fill leaves buffer
// A's text showing through. Buffer A uses uppercase letters and buffer B
// lowercase, so any bleed-through is unmistakable in the golden.
//
// The no-Clear sequence is safe in the real editor because
// cmd/micro/micro.go fills the whole screen once per frame before any
// pane draws; this test drops that step only to pin down the raw
// two-render result byte for byte. The golden lives at testdata root (not
// a group directory), so TestRender's testdata/*/*.golden glob skips it;
// it is compared whole-file and rewritten by -update here.
func TestRenderDeadZoneStale(t *testing.T) {
	const winX, winY, winW, winH = 2, 1, 36, 10

	screen.Screen.Clear()
	screen.Screen.Sync()

	// Buffer A wraps at the full pane width (no softwrapcolumn), so its
	// content genuinely spans columns 0-35, including the range that
	// becomes buffer B's dead zone. Each line is a distinct 40-character
	// slice of one continuous uppercase alphabet cycle, so a stale row is
	// identifiable by its glyphs.
	settingsA := map[string]interface{}{
		"softwrap":   true,
		"ruler":      false,
		"diffgutter": false,
		"scrollbar":  false,
	}
	textA := strings.Join([]string{
		"ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMN",
		"OPQRSTUVWXYZABCDEFGHIJKLMNOPQRSTUVWXYZAB",
		"CDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMNOP",
	}, "\n")
	bufA := buffer.NewBufferFromString(textA, "", buffer.BTDefault)
	for k, v := range settingsA {
		bufA.Settings[k] = v
	}
	w := NewBufWindow(winX, winY, winW, winH, bufA)
	w.Display()

	// Buffer B narrows to softwrapcolumn=20, leaving a dead zone in
	// columns 20-35. Its 25 lowercase characters wrap once, so row 0
	// exercises the intermediate wrap-row dead-zone fill, not only the
	// trailing per-line fill.
	settingsB := map[string]interface{}{
		"softwrap":       true,
		"softwrapcolumn": float64(20),
		"ruler":          false,
		"diffgutter":     false,
		"scrollbar":      false,
	}
	bufB := buffer.NewBufferFromString(
		"abcdefghijklmnopqrstuvwxy", "", buffer.BTDefault)
	for k, v := range settingsB {
		bufB.Settings[k] = v
	}
	w.SetBuffer(bufB)

	// Deliberately no screen.Screen.Clear() here: that is the point of
	// this test.
	w.Display()

	got := serializeScreen(t, sim, false)

	const goldenPath = "testdata/swc_dead_zone_stale.golden"
	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, string(want), got)
}
