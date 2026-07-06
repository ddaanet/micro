package display

import (
	"strings"
	"testing"

	"github.com/micro-editor/micro/v2/internal/buffer"
	"github.com/micro-editor/micro/v2/internal/config"
	ulua "github.com/micro-editor/micro/v2/internal/lua"
	"github.com/micro-editor/micro/v2/internal/util"
	"github.com/stretchr/testify/assert"
	lua "github.com/yuin/gopher-lua"
)

func init() {
	ulua.L = lua.NewState()
	config.InitRuntimeFiles(false)
	config.InitGlobalSettings()
}

// newWrapTestWindow builds a BufWindow whose bufWidth exactly equals paneWidth
// (ruler, diffgutter, scrollbar all off, so gutterOffset is 0), with softwrap
// on and the given wordwrap/tabsize/softwrapcolumn settings. Using the default
// `statusline: true` setting means updateDisplayInfo never touches
// screen.Screen, so this works without any terminal, real or simulated.
func newWrapTestWindow(
	text string, paneWidth int, wordwrap bool, tabsize int, softwrapcolumn int,
) *BufWindow {
	buf := buffer.NewBufferFromString(text, "", buffer.BTDefault)
	buf.Settings["softwrap"] = true
	buf.Settings["wordwrap"] = wordwrap
	buf.Settings["tabsize"] = float64(tabsize)
	buf.Settings["softwrapcolumn"] = float64(softwrapcolumn)
	buf.Settings["ruler"] = false
	buf.Settings["diffgutter"] = false
	buf.Settings["scrollbar"] = false

	w := NewBufWindow(0, 0, paneWidth, 10, buf)
	w.updateDisplayInfo()
	return w
}

// assertRoundTrip checks that mapping every buffer column on line 0 to a
// visual location and back returns the original column — the invariant that
// cursor up/down, mouse clicks, and selection all rely on. This is the
// property that broke for the wrapindent PR (#3107) when it injected phantom
// leading columns on continuation rows; softwrapcolumn does not, so it must
// hold here too.
func assertRoundTrip(t *testing.T, w *BufWindow, lineLen int) {
	t.Helper()
	for x := 0; x <= lineLen; x++ {
		loc := buffer.Loc{X: x, Y: 0}
		vloc := w.VLocFromLoc(loc)
		got := w.LocFromVLoc(vloc)
		assert.Equal(t, x, got.X,
			"round-trip mismatch at column %d (vloc=%+v)", x, vloc)
	}
}

// assertRoundTripLine is like assertRoundTrip but exercises an arbitrary
// buffer line y, covering the multi-line path that assertRoundTrip's
// line-0-only case doesn't reach.
func assertRoundTripLine(t *testing.T, w *BufWindow, lineLen int, y int) {
	t.Helper()
	for x := 0; x <= lineLen; x++ {
		loc := buffer.Loc{X: x, Y: y}
		vloc := w.VLocFromLoc(loc)
		got := w.LocFromVLoc(vloc)
		assert.Equal(t, x, got.X,
			"round-trip mismatch at column %d, line %d (vloc=%+v)", x, y, vloc)
		assert.Equal(t, y, got.Y,
			"round-trip mismatch: line drifted at column %d, "+
				"line %d (vloc=%+v)", x, y, vloc)
	}
}

func TestSoftWrapColumnRoundTrip(t *testing.T) {
	line := strings.Repeat("A", 60)

	t.Run("off (0) wraps at the full pane width", func(t *testing.T) {
		w := newWrapTestWindow(line, 40, false, 4, 0)
		assertRoundTrip(t, w, len(line))
		assert.Equal(t, 0, w.VLocFromLoc(buffer.Loc{X: 39, Y: 0}).Row)
		assert.Equal(t, 1, w.VLocFromLoc(buffer.Loc{X: 40, Y: 0}).Row)
	})

	t.Run("narrower than the pane wraps at the configured column", func(t *testing.T) {
		w := newWrapTestWindow(line, 58, false, 4, 30)
		assertRoundTrip(t, w, len(line))
		assert.Equal(t, 0, w.VLocFromLoc(buffer.Loc{X: 29, Y: 0}).Row)
		assert.Equal(t, 1, w.VLocFromLoc(buffer.Loc{X: 30, Y: 0}).Row)
	})

	t.Run("wider than the pane clamps to the pane width", func(t *testing.T) {
		w := newWrapTestWindow(line, 18, false, 4, 30)
		assertRoundTrip(t, w, len(line))
		assert.Equal(t, 0, w.VLocFromLoc(buffer.Loc{X: 17, Y: 0}).Row)
		assert.Equal(t, 1, w.VLocFromLoc(buffer.Loc{X: 18, Y: 0}).Row)
	})

	t.Run("wordwrap on with a word longer than the column", func(t *testing.T) {
		longWord := "short " + strings.Repeat("B", 50)
		w := newWrapTestWindow(longWord, 58, true, 4, 10)
		assertRoundTrip(t, w, len(longWord))
	})

	t.Run("tabs straddling the wrap column", func(t *testing.T) {
		tabby := "a\tb\tc\td\te\tf\tg\th"
		w := newWrapTestWindow(tabby, 58, false, 4, 10)
		assertRoundTrip(t, w, util.CharacterCount([]byte(tabby)))
	})

	t.Run("multi-line buffer round-trips on non-zero lines", func(t *testing.T) {
		lineA := strings.Repeat("A", 60)
		lineB := strings.Repeat("B", 45)
		text := lineA + "\n" + lineB
		w := newWrapTestWindow(text, 58, false, 4, 20)
		assertRoundTripLine(t, w, len(lineA), 0)
		assertRoundTripLine(t, w, len(lineB), 1)
	})

	t.Run("non-zero gutter (ruler on) round-trips", func(t *testing.T) {
		// Mirrors newWrapTestWindow's construction, but with ruler enabled
		// so gutterOffset is non-zero and bufWidth < paneWidth.
		buf := buffer.NewBufferFromString(line, "", buffer.BTDefault)
		buf.Settings["softwrap"] = true
		buf.Settings["wordwrap"] = false
		buf.Settings["tabsize"] = float64(4)
		buf.Settings["softwrapcolumn"] = float64(20)
		buf.Settings["ruler"] = true
		buf.Settings["diffgutter"] = false
		buf.Settings["scrollbar"] = false

		w := NewBufWindow(0, 0, 58, 10, buf)
		w.updateDisplayInfo()

		assert.NotEqual(t, 0, w.gutterOffset,
			"expected ruler to produce a non-zero gutter")
		assertRoundTrip(t, w, len(line))
	})
}

// TestSoftWrapColumnRelocation confirms that changing softwrapcolumn on a
// live buffer via Buf.SetOption (the same path the `set` command uses)
// triggers a re-wrap. BufWindow.SetBuffer registers an OptionCallback that,
// for "softwrapcolumn", calls updateDisplayInfo() (which recomputes
// wrapWidth) and Relocate(). This pins that a runtime option change actually
// reflows text, not just a freshly constructed window.
func TestSoftWrapColumnRelocation(t *testing.T) {
	line := strings.Repeat("A", 60)
	w := newWrapTestWindow(line, 58, false, 4, 0)

	loc := buffer.Loc{X: 40, Y: 0}
	rowBefore := w.VLocFromLoc(loc).Row
	assert.Equal(t, 0, rowBefore,
		"with softwrapcolumn off, column 40 should stay on row 0 "+
			"within a 58-wide pane")

	err := w.Buf.SetOption("softwrapcolumn", "20")
	assert.NoError(t, err)

	rowAfter := w.VLocFromLoc(loc).Row
	assert.NotEqual(t, rowBefore, rowAfter,
		"changing softwrapcolumn at runtime should trigger a re-wrap")
	assert.Equal(t, 2, rowAfter,
		"column 40 should now be on row 2 (wrapping every 20 columns)")
}
