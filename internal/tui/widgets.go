package tui

import "strings"

// Width returns the display width of s in terminal cells.
func Width(s string) int { return stringWidth(s) }

// Truncate shortens s to at most w cells, adding an ellipsis when cut.
func Truncate(s string, w int) string { return trunc(s, w) }

// Pad appends spaces so that s occupies exactly w cells.
func Pad(s string, w int) string { return pad(s, w) }

// SliceCells returns the window of s starting at cell offset start,
// at most width cells wide. Used to scroll long rows horizontally on
// narrow phone screens.
func SliceCells(s string, start, width int) string {
	if width <= 0 || start < 0 {
		if width <= 0 {
			return ""
		}
		start = 0
	}
	var b strings.Builder
	col, used := 0, 0
	for _, r := range s {
		w := runeWidth(r)
		if col+w <= start {
			col += w
			continue
		}
		if col < start {
			// A wide rune straddling the window edge: skip it whole
			// rather than drawing half a cell.
			col += w
			continue
		}
		if used+w > width {
			break
		}
		b.WriteRune(r)
		used += w
		col += w
	}
	return b.String()
}

// WrapLines splits s into lines of at most width cells, preferring word
// boundaries and honouring embedded newlines. Overlong words are hard-cut.
func WrapLines(s string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	flush := func() {
		lines = append(lines, strings.TrimRight(cur.String(), " "))
		cur.Reset()
		curW = 0
	}
	word := []rune{}
	wordW := 0
	flushWord := func() {
		if wordW == 0 {
			return
		}
		// A pending separator space is already counted in curW.
		if curW > 0 && curW+wordW > width {
			flush()
		}
		for _, r := range word {
			w := runeWidth(r)
			if curW+w > width {
				flush()
			}
			cur.WriteRune(r)
			curW += w
		}
		word = word[:0]
		wordW = 0
	}
	for _, r := range s {
		switch {
		case r == '\n':
			flushWord()
			flush()
		case r == ' ' || r == '\t':
			flushWord()
			if curW+1 > width {
				flush()
			} else if curW > 0 || cur.String() != "" {
				cur.WriteByte(' ')
				curW++
			}
		default:
			word = append(word, r)
			wordW += runeWidth(r)
		}
	}
	flushWord()
	flush()
	return lines
}

// pad appends spaces so s occupies exactly width cells.
func pad(s string, width int) string {
	w := stringWidth(s)
	if w >= width {
		return s
	}
	out := make([]byte, 0, len(s)+width-w)
	out = append(out, s...)
	for i := w; i < width; i++ {
		out = append(out, ' ')
	}
	return string(out)
}

// trunc runs a string to at most width cells, adding an ellipsis when cut.
func trunc(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if stringWidth(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	out := make([]rune, 0, width)
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > width-1 {
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "…"
}

// ListItem is one row of a List.
type ListItem struct {
	Title string
	Desc  string
	// Badge is short text pinned to the right edge of the row.
	Badge      string
	BadgeColor int
	// ID carries the caller's own payload (plugin name, action id, ...).
	ID string
}

// List is a scrollable selection list. Sel moves vertically; X scrolls
// the selected row horizontally (←/→) so long titles and descriptions
// stay readable on narrow phone screens.
type List struct {
	Items  []ListItem
	Sel    int
	Offset int
	X      int
	// lastW remembers the text width of the last drawn row, so Key
	// knows how far the selected row may scroll. Zero before the
	// first Draw — then scrolling stays off (safe default).
	lastW int
}

// Select moves the cursor by delta rows.
func (l *List) Select(delta int) {
	if len(l.Items) == 0 {
		l.Sel = 0
		return
	}
	l.Sel += delta
	if l.Sel < 0 {
		l.Sel = 0
	}
	if l.Sel >= len(l.Items) {
		l.Sel = len(l.Items) - 1
	}
	l.X = 0
}

// First jumps to the start of the list.
func (l *List) First() { l.Sel = 0; l.X = 0 }

// Last jumps to the end of the list.
func (l *List) Last() {
	if len(l.Items) > 0 {
		l.Sel = len(l.Items) - 1
	}
	l.X = 0
}

// fullText is everything the row can show when scrolled.
func (l *List) fullText(i int) string {
	if i < 0 || i >= len(l.Items) {
		return ""
	}
	it := l.Items[i]
	if it.Desc == "" {
		return it.Title
	}
	return it.Title + " · " + it.Desc
}

// maxX is how far the selected row can scroll: the overflow beyond
// the last drawn width. Zero when the row fits or nothing was drawn yet.
func (l *List) maxX() int {
	if l.lastW <= 0 {
		return 0
	}
	if fw := stringWidth(l.fullText(l.Sel)); fw > l.lastW {
		return fw - l.lastW
	}
	return 0
}

// Key handles the navigation keys. It reports whether the key was used.
func (l *List) Key(k Key) bool {
	switch k.Type {
	case KeyUp:
		l.Select(-1)
	case KeyDown:
		l.Select(1)
	case KeyPgUp:
		l.Select(-8)
	case KeyPgDn:
		l.Select(8)
	case KeyHome:
		l.First()
	case KeyEnd:
		l.Last()
	case KeyLeft:
		if l.maxX() == 0 || l.X <= 0 {
			return false
		}
		l.X -= 4
		if l.X < 0 {
			l.X = 0
		}
	case KeyRight:
		m := l.maxX()
		if m == 0 || l.X >= m {
			return false
		}
		l.X += 4
		if l.X > m {
			l.X = m
		}
	default:
		return false
	}
	return true
}

// Current returns the selected item, if any.
func (l *List) Current() (ListItem, bool) {
	if l.Sel < 0 || l.Sel >= len(l.Items) {
		return ListItem{}, false
	}
	return l.Items[l.Sel], true
}

// Draw paints the list inside the (x, y, w, h) rectangle, scrolling so the
// selection stays visible.
func (l *List) Draw(f *Frame, x, y, w, h int, focused bool) {
	if h <= 0 || w <= 0 || len(l.Items) == 0 {
		return
	}
	if l.Sel < l.Offset {
		l.Offset = l.Sel
	}
	if l.Sel >= l.Offset+h {
		l.Offset = l.Sel - h + 1
	}
	if l.Offset < 0 {
		l.Offset = 0
	}

	for row := 0; row < h; row++ {
		idx := l.Offset + row
		if idx >= len(l.Items) {
			break
		}
		it := l.Items[idx]
		selected := idx == l.Sel && focused

		rowStyle := Style{Fg: ColorText}
		if selected {
			rowStyle = Style{Fg: ColorSelectF, Bg: ColorSelect, Bold: true}
		}
		f.Fill(x, y+row, w, 1, ' ', rowStyle)

		marker := "  "
		if selected {
			marker = "▸ "
		}
		col := x
		f.TextLimit(col, y+row, 2, marker, rowStyle)
		col += 2

		badgeW := 0
		if it.Badge != "" {
			badgeW = Width(it.Badge) + 1
		}
		titleW := w - 2 - badgeW
		if titleW < 4 {
			titleW = w - 2
			badgeW = 0
		}
		full := it.Title
		if it.Desc != "" {
			full += " · " + it.Desc
		}
		l.lastW = titleW
		if m := l.maxX(); l.X > m {
			l.X = m
		}
		if l.X < 0 {
			l.X = 0
		}
		if stringWidth(full) <= titleW && l.X == 0 {
			title := trunc(it.Title, titleW)
			f.TextLimit(col, y+row, titleW, title, rowStyle)
			if it.Desc != "" && !selected {
				// A faint description trails the title when there is room.
				rest := titleW - Width(title) - 1
				if rest > 6 {
					f.TextLimit(col+Width(title)+1, y+row, rest, "· "+trunc(it.Desc, rest-2),
						Style{Fg: ColorDim, Bg: rowStyle.Bg})
				}
			}
		} else {
			// Overflow: a scrollable window over "title · desc" with
			// edge markers, driven by ←/→ (see List.Key).
			fw := stringWidth(full)
			cx, avail := col, titleW
			if l.X > 0 {
				f.TextLimit(cx, y+row, 1, "◀", Style{Fg: ColorDim, Bg: rowStyle.Bg})
				cx++
				avail--
			}
			right := 0
			if l.X+avail < fw {
				right = 1
			}
			win := SliceCells(full, l.X, avail-right)
			f.TextLimit(cx, y+row, avail-right, win, rowStyle)
			if right > 0 {
				f.TextLimit(cx+avail-right, y+row, 1, "▶", Style{Fg: ColorDim, Bg: rowStyle.Bg})
			}
		}
		if badgeW > 0 {
			bs := Style{Fg: ColorAccent2, Bg: rowStyle.Bg, Bold: true}
			if it.BadgeColor != 0 {
				bs.Fg = it.BadgeColor
			}
			f.TextLimit(x+w-badgeW, y+row, badgeW, it.Badge, bs)
		}
	}
}

// ---- text input ----

// Input is a single-line editable text field drawn inside a small box.
type Input struct {
	Value       []rune
	Cursor      int
	Mask        rune // when non-zero every character is drawn as this rune
	Title       string
	Placeholder string
	// Offset keeps the caret visible when the value is longer than the box.
	Offset int
}

// String returns the raw value.
func (i *Input) String() string { return string(i.Value) }

// Reset clears the field.
func (i *Input) Reset() {
	i.Value = nil
	i.Cursor = 0
	i.Offset = 0
}

// Set replaces the value and puts the cursor at the end.
func (i *Input) Set(s string) {
	i.Value = []rune(s)
	i.Cursor = len(i.Value)
	i.Offset = 0
}

// Key edits the field. It reports whether the key was consumed.
func (i *Input) Key(k Key) bool {
	switch k.Type {
	case KeyRune:
		if k.Rune < 32 {
			return false
		}
		tail := append([]rune{k.Rune}, i.Value[i.Cursor:]...)
		i.Value = append(i.Value[:i.Cursor], tail...)
		i.Cursor++
	case KeyBackspace:
		if i.Cursor > 0 {
			i.Value = append(i.Value[:i.Cursor-1], i.Value[i.Cursor:]...)
			i.Cursor--
		}
	case KeyDelete:
		if i.Cursor < len(i.Value) {
			i.Value = append(i.Value[:i.Cursor], i.Value[i.Cursor+1:]...)
		}
	case KeyLeft:
		if i.Cursor > 0 {
			i.Cursor--
		}
	case KeyRight:
		if i.Cursor < len(i.Value) {
			i.Cursor++
		}
	case KeyHome:
		i.Cursor = 0
	case KeyEnd:
		i.Cursor = len(i.Value)
	case KeyCtrlU:
		i.Value = nil
		i.Cursor = 0
	case KeyCtrlW:
		start := i.Cursor
		for start > 0 && i.Value[start-1] == ' ' {
			start--
		}
		for start > 0 && i.Value[start-1] != ' ' {
			start--
		}
		i.Value = append(i.Value[:start], i.Value[i.Cursor:]...)
		i.Cursor = start
	default:
		return false
	}
	return true
}

// Height is how many rows Draw needs.
func (i *Input) Height() int { return 3 }

// Draw paints the field at (x, y) with the given width.
func (i *Input) Draw(f *Frame, x, y, w int, focused bool) {
	if w < 6 {
		return
	}
	border := Style{Fg: ColorBorder}
	if focused {
		border = Style{Fg: ColorAccent, Bold: true}
	}
	inner := w - 2

	// Visible slice of the value that keeps the cursor on screen.
	display := make([]rune, len(i.Value))
	if i.Mask != 0 {
		for idx := range display {
			display[idx] = i.Mask
		}
	} else {
		copy(display, i.Value)
	}
	// One cell is reserved for the caret.
	visible := inner - 1
	if visible < 1 {
		visible = 1
	}
	if i.Cursor < i.Offset {
		i.Offset = i.Cursor
	}
	if i.Cursor > i.Offset+visible-1 {
		i.Offset = i.Cursor - visible + 1
	}
	if i.Offset > len(display) {
		i.Offset = len(display)
	}
	if i.Offset < 0 {
		i.Offset = 0
	}

	f.Box(x, y, w, 3, border, i.Title)

	line := y + 1
	cellX := x + 1
	row := display[i.Offset:]
	if len(row) == 0 && i.Placeholder != "" && len(i.Value) == 0 {
		f.TextLimit(cellX, line, inner, trunc(i.Placeholder, inner), Style{Fg: ColorFaint})
	}

	for idx := 0; idx < visible && idx < len(row); idx++ {
		r := row[idx]
		if r == 0 {
			r = ' '
		}
		st := Style{Fg: ColorText}
		if i.Offset+idx == i.Cursor && focused {
			st = Style{Fg: ColorSelectF, Bg: ColorAccent, Bold: true}
		}
		f.Set(cellX+idx, line, r, st)
	}
	if i.Cursor == i.Offset+len(row) && focused && len(row) < visible {
		f.Set(cellX+len(row), line, ' ', Style{Bg: ColorAccent})
	}
}

// Bar draws a progress bar of width w filled to frac (0..1).
func Bar(f *Frame, x, y, w int, frac float64, st Style) {
	if w < 3 {
		return
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * float64(w))
	for i := 0; i < w; i++ {
		r := '░'
		if i < filled {
			r = '█'
		}
		f.Set(x+i, y, r, st)
	}
}
