package tui

import "fmt"

// Style describes the colors and attributes of a cell.
// Fg and Bg are 256-color palette indices, or ColorDefault for the terminal
// default. Any negative value means "default".
type Style struct {
	Fg      int
	Bg      int
	Bold    bool
	Dim     bool
	Italic  bool
	Under   bool
	Reverse bool
}

// Palette indices used across Aurora's screens.
const (
	ColorDefault = -1

	ColorAccent  = 141 // lavender — the Aurora brand color
	ColorAccent2 = 51  // cyan
	ColorGood    = 41
	ColorWarn    = 214
	ColorBad     = 203
	ColorText    = 252
	ColorDim     = 244
	ColorFaint   = 238
	ColorBorder  = 62
	ColorSelect  = 57  // selection background
	ColorSelectF = 231 // selection foreground
	ColorKey     = 176 // shortcut badges
)

type cell struct {
	r  rune
	st Style
}

// Frame is a rune framebuffer the size of the terminal.
type Frame struct {
	w, h  int
	cells []cell
}

// NewFrame allocates an empty frame.
func NewFrame(w, h int) *Frame {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return &Frame{w: w, h: h, cells: make([]cell, w*h)}
}

// Width returns the frame width in cells.
func (f *Frame) Width() int { return f.w }

// Height returns the frame height in cells.
func (f *Frame) Height() int { return f.h }

// Clear fills the whole frame.
func (f *Frame) Clear(st Style) { f.Fill(0, 0, f.w, f.h, ' ', st) }

// Fill paints a rectangle.
func (f *Frame) Fill(x, y, w, h int, r rune, st Style) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			f.Set(i, j, r, st)
		}
	}
}

// Set writes a single cell, ignoring out-of-bounds coordinates.
func (f *Frame) Set(x, y int, r rune, st Style) {
	if x < 0 || y < 0 || x >= f.w || y >= f.h {
		return
	}
	f.cells[y*f.w+x] = cell{r: r, st: st}
}

// StyleAt returns the style of a cell (used by widgets that need to know
// what is already painted underneath them).
func (f *Frame) StyleAt(x, y int) Style {
	if x < 0 || y < 0 || x >= f.w || y >= f.h {
		return Style{}
	}
	return f.cells[y*f.w+x].st
}

// Text draws a single line and clips it to the frame edge.
func (f *Frame) Text(x, y int, s string, st Style) {
	f.TextLimit(x, y, f.w-x, s, st)
}

// TextLimit draws a line clipped to at most limit cells.
func (f *Frame) TextLimit(x, y, limit int, s string, st Style) {
	if limit <= 0 {
		return
	}
	used := 0
	for _, r := range s {
		w := runeWidth(r)
		if used+w > limit {
			return
		}
		f.Set(x+used, y, r, st)
		// Wide runes occupy the following cell too, so it does not get
		// overwritten by the next character.
		if w == 2 {
			f.Set(x+used+1, y, ' ', st)
		}
		used += w
	}
}

// Center draws a line horizontally centered inside [x, x+w).
func (f *Frame) Center(x, w, y int, s string, st Style) {
	width := stringWidth(s)
	start := x + (w-width)/2
	if start < x {
		start = x
	}
	f.Text(start, y, s, st)
}

// FillLine draws s from x, padding with spaces up to w cells so a previous
// longer line cannot bleed through.
func (f *Frame) FillLine(x, y, w int, s string, st Style) {
	f.Fill(x, y, w, 1, ' ', st)
	f.TextLimit(x, y, w, s, st)
}

// Wrap renders text wrapped to width starting at (x, y) and returns the
// number of lines it used.
func (f *Frame) Wrap(x, y, width, maxLines int, s string, st Style) int {
	if width <= 0 || maxLines <= 0 {
		return 0
	}
	lines := 0
	col := 0
	lineStart := y
	word := []rune{}
	wordW := 0
	space := func() {
		if col+1 <= width {
			f.Set(x+col, lineStart, ' ', st)
			col++
		}
	}
	flushWord := func() {
		if wordW == 0 {
			return
		}
		if col+wordW > width {
			lines++
			lineStart = y + lines
			col = 0
			if lines >= maxLines {
				word, wordW = word[:0], 0
				return
			}
		}
		for _, r := range word {
			f.Set(x+col, lineStart, r, st)
			col++
		}
		word, wordW = word[:0], 0
	}
	for _, r := range s {
		switch {
		case r == '\n':
			flushWord()
			lines++
			lineStart = y + lines
			col = 0
			if lines >= maxLines {
				return lines
			}
		case r == ' ' || r == '\t':
			flushWord()
			space()
		default:
			word = append(word, r)
			wordW += runeWidth(r)
			if wordW > width {
				flushWord()
				if lines >= maxLines {
					return lines
				}
			}
		}
		if lineStart >= y+maxLines {
			return lines
		}
	}
	flushWord()
	return lines + 1
}

// Box draws a rounded single-line border, optionally with a title.
func (f *Frame) Box(x, y, w, h int, st Style, title string) {
	if w < 2 || h < 2 {
		return
	}
	f.Set(x, y, '╭', st)
	f.Set(x+w-1, y, '╮', st)
	f.Set(x, y+h-1, '╰', st)
	f.Set(x+w-1, y+h-1, '╯', st)
	for i := 1; i < w-1; i++ {
		f.Set(x+i, y, '─', st)
		f.Set(x+i, y+h-1, '─', st)
	}
	for j := 1; j < h-1; j++ {
		f.Set(x, y+j, '│', st)
		f.Set(x+w-1, y+j, '│', st)
	}
	if title != "" && w > 6 {
		label := " " + title + " "
		f.TextLimit(x+1, y, w-2, label, Style{Fg: st.Fg, Bold: true})
	}
}

// Panel is a box with a filled background.
func (f *Frame) Panel(x, y, w, h int, border, bg Style, title string) {
	inner := border
	inner.Bg = bg.Bg
	f.Fill(x+1, y+1, w-2, h-2, ' ', inner)
	f.Box(x, y, w, h, border, title)
}

// HLine draws a horizontal rule.
func (f *Frame) HLine(x, y, w int, st Style) {
	for i := 0; i < w; i++ {
		f.Set(x+i, y, '─', st)
	}
}

// Rule draws a faint divider across the full width.
func (f *Frame) Rule(y int) {
	for i := 0; i < f.w; i++ {
		f.Set(i, y, '─', Style{Fg: ColorFaint})
	}
}

// ---- rendering ----

// Render produces the escape sequence that turns prev into this frame.
// It returns the new baseline, ready to be passed in on the next frame.
func (f *Frame) Render(prev []cell, b *byteBuf) []cell {
	full := prev == nil || len(prev) != len(f.cells)
	if full {
		prev = make([]cell, len(f.cells))
		b.WriteString("\x1b[2J")
	}
	for y := 0; y < f.h; y++ {
		x := 0
		for x < f.w {
			i := y*f.w + x
			if !full && prev[i] == f.cells[i] {
				x++
				continue
			}
			st := f.cells[i].st
			b.Printf("\x1b[%d;%dH", y+1, x+1)
			b.WriteString(st.ansi())
			for x < f.w {
				j := y*f.w + x
				if !full && prev[j] == f.cells[j] {
					break
				}
				c := f.cells[j]
				if c.st != st {
					b.WriteString(c.st.ansi())
					st = c.st
				}
				r := c.r
				if r < 32 || r == 0x7f {
					r = ' '
				}
				b.WriteRune(r)
				prev[j] = c
				x++
			}
			b.WriteString("\x1b[0m")
		}
	}
	return prev
}

// byteBuf is a growable buffer with a couple of helpers, kept allocation-free
// across frames.
type byteBuf struct {
	buf []byte
}

func (b *byteBuf) WriteString(s string) { b.buf = append(b.buf, s...) }
func (b *byteBuf) WriteRune(r rune) {
	switch {
	case r < 0x80:
		b.buf = append(b.buf, byte(r))
	case r < 0x800:
		b.buf = append(b.buf, byte(0xC0|r>>6), byte(0x80|r&0x3F))
	default:
		b.buf = append(b.buf, byte(0xE0|r>>12), byte(0x80|(r>>6)&0x3F), byte(0x80|r&0x3F))
	}
}
func (b *byteBuf) Printf(format string, args ...any) { b.buf = fmt.Appendf(b.buf, format, args...) }
func (b *byteBuf) Bytes() []byte                     { return b.buf }
func (b *byteBuf) Reset()                            { b.buf = b.buf[:0] }

// ansi renders the style as an SGR sequence.
func (s Style) ansi() string {
	out := "\x1b[0"
	if s.Bold {
		out += ";1"
	}
	if s.Dim {
		out += ";2"
	}
	if s.Italic {
		out += ";3"
	}
	if s.Under {
		out += ";4"
	}
	if s.Reverse {
		out += ";7"
	}
	if s.Fg >= 0 {
		out += fmt.Sprintf(";38;5;%d", s.Fg)
	}
	if s.Bg >= 0 {
		out += fmt.Sprintf(";48;5;%d", s.Bg)
	}
	return out + "m"
}

// runeWidth returns how many cells a rune occupies. Aurora's UI sticks to
// box drawing and Latin text (width 1); the usual wide ranges are handled
// so stray emoji in a plugin description cannot shift the layout.
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32 || r == 0x7f:
		// Rendered as a space, but it still owns one cell.
		return 1
	case r >= 0x1100 && (r <= 0x115F || // Hangul Jamo
		r == 0x2329 || r == 0x232A ||
		(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1F64F) ||
		(r >= 0x1F900 && r <= 0x1F9FF) ||
		(r >= 0x20000 && r <= 0x3FFFD)):
		return 2
	default:
		return 1
	}
}

// stringWidth returns the display width of s.
func stringWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}
