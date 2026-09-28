package tui

import (
	"sync"
	"time"
)

// View is a full-screen state of the UI: it paints itself into a frame and
// reacts to keypresses.
type View interface {
	Draw(f *Frame)
	OnKey(k Key)
}

// Ticker is implemented by views that want a periodic callback (spinners,
// live data refresh).
type Ticker interface {
	Tick()
}

// App owns the terminal, the render loop and the current view.
type App struct {
	term *terminal

	keys chan Key
	wake chan struct{}
	done chan struct{}

	mu   sync.Mutex
	view View

	prev []cell
	buf  byteBuf
}

// NewApp switches the terminal into raw mode and the alternate screen.
// It returns ErrNotTTY when the process has no usable terminal.
func NewApp() (*App, error) {
	t, err := openTerminal()
	if err != nil {
		return nil, err
	}
	return &App{
		term: t,
		keys: make(chan Key, 32),
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}, nil
}

// Size returns the current terminal size.
func (a *App) Size() (int, int) {
	a.term.resize()
	return a.term.w, a.term.h
}

// SetView swaps the visible screen. It is safe to call from a key handler
// or from a background goroutine (after Wake).
func (a *App) SetView(v View) {
	a.mu.Lock()
	a.view = v
	a.mu.Unlock()
	a.Wake()
}

// Current returns the active view.
func (a *App) Current() View {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.view
}

// Wake asks the render loop for a redraw. It never blocks.
func (a *App) Wake() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Quit stops the run loop and restores the terminal.
func (a *App) Quit() {
	select {
	case <-a.done:
	default:
		close(a.done)
	}
}

// Run renders until Quit is called or stdin closes.
func (a *App) Run(view View) error {
	a.SetView(view)
	defer a.term.close()

	// Alternate screen, hide the cursor, clear.
	a.term.out.WriteString("\x1b[?1049h\x1b[?25l\x1b[H")
	defer a.term.out.WriteString("\x1b[?1049l\x1b[?25h\x1b[0m")

	go keyReader(a.term.in.Read, a.keys)

	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()

	a.draw()
	for {
		select {
		case <-a.done:
			return nil
		case k, ok := <-a.keys:
			if !ok {
				return nil
			}
			// The view sees every key first; Ctrl+C still quits unless the
			// view consumed it by switching screens.
			prev := a.Current()
			if prev != nil {
				prev.OnKey(k)
			}
			if k.Type == KeyCtrlC && a.Current() == prev {
				a.Quit()
			}
		case <-tick.C:
			if v := a.Current(); v != nil {
				if t, ok := v.(Ticker); ok {
					t.Tick()
				}
			}
		case <-a.wake:
		}
		a.draw()
	}
}

func (a *App) draw() {
	v := a.Current()
	if v == nil {
		return
	}
	a.term.resize()
	f := NewFrame(a.term.w, a.term.h)
	f.Clear(Style{Fg: ColorText})
	v.Draw(f)

	a.buf.Reset()
	a.prev = f.Render(a.prev, &a.buf)
	if len(a.buf.Bytes()) > 0 {
		_, _ = a.term.out.Write(a.buf.Bytes())
	}
}

// ---- small shared helpers ----

// spinnerFrames are the frames of the classic braille spinner.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// Spinner renders one frame of the braille spinner.
func Spinner(frame int) string {
	return string(spinnerFrames[frame%len(spinnerFrames)])
}
