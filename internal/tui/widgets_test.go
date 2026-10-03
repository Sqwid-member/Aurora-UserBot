package tui

import "testing"

func TestSliceCells(t *testing.T) {
	if got := SliceCells("hello world", 0, 5); got != "hello" {
		t.Fatalf("got %q", got)
	}
	if got := SliceCells("hello world", 6, 5); got != "world" {
		t.Fatalf("got %q", got)
	}
	if got := SliceCells("hi", 0, 10); got != "hi" {
		t.Fatalf("got %q", got)
	}
	if got := SliceCells("hi", 5, 10); got != "" {
		t.Fatalf("got %q", got)
	}
	// Wide rune straddling the window edge is skipped whole.
	if got := SliceCells("a\u4e2db", 2, 5); got != "b" {
		t.Fatalf("got %q", got)
	}
}

func TestWrapLines(t *testing.T) {
	lines := WrapLines("foo bar baz", 7)
	if len(lines) != 2 || lines[0] != "foo bar" || lines[1] != "baz" {
		t.Fatalf("got %q", lines)
	}
	lines = WrapLines("a\nb", 10)
	if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("got %q", lines)
	}
	// Overlong word is hard-cut, never wider than width.
	lines = WrapLines("abcdefghij", 4)
	for _, l := range lines {
		if stringWidth(l) > 4 {
			t.Fatalf("line too wide: %q", l)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("got %q", lines)
	}
}

func TestListHorizontalScroll(t *testing.T) {
	l := List{Items: []ListItem{{Title: "long title here", Desc: "and a description"}}}
	if l.Key(RuneKey('x')) {
		t.Fatal("unrelated key consumed")
	}
	// Before the first Draw the viewport is unknown: no scrolling.
	if l.Key(Key{Type: KeyRight}) {
		t.Fatal("Right consumed before Draw")
	}
	f := NewFrame(20, 5)
	l.Draw(f, 0, 0, 20, 5, true)
	if !l.Key(Key{Type: KeyRight}) {
		t.Fatal("Right not consumed on overflowing row")
	}
	if l.X == 0 {
		t.Fatal("X did not move")
	}
	if !l.Key(Key{Type: KeyLeft}) {
		t.Fatal("Left not consumed while scrolled")
	}
	l.Select(1) // vertical move resets X
	if l.X != 0 {
		t.Fatal("X not reset")
	}
	short := List{Items: []ListItem{{Title: "ok"}}}
	short.Draw(f, 0, 0, 20, 5, true)
	if short.Key(Key{Type: KeyRight}) {
		t.Fatal("Right consumed on short row")
	}
}
