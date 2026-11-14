package dashboard

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"seavxrouter/internal/httpapi"
)

// tuiKey is one decoded key press; printable keys keep their byte value.
type tuiKey int

const (
	keyEsc tuiKey = 256 + iota
	keyUp
	keyDown
	keyEnter
)

// parseKeys decodes arrow-key escape sequences (CSI and SS3) from one read.
func parseKeys(buf []byte) []tuiKey {
	keys := make([]tuiKey, 0, len(buf))
	for i := 0; i < len(buf); i++ {
		if buf[i] == 0x1b && i+2 < len(buf) && (buf[i+1] == '[' || buf[i+1] == 'O') {
			switch buf[i+2] {
			case 'A':
				keys = append(keys, keyUp)
				i += 2
				continue
			case 'B':
				keys = append(keys, keyDown)
				i += 2
				continue
			}
		}
		switch buf[i] {
		case 0x1b:
			keys = append(keys, keyEsc)
		case '\r', '\n':
			keys = append(keys, keyEnter)
		default:
			keys = append(keys, tuiKey(buf[i]))
		}
	}
	return keys
}

// moveSelection advances the recent-list cursor without wrapping: pressing up
// at the top or down at the bottom keeps the cursor in place.
func moveSelection(selected, delta, n int) int {
	if n <= 0 {
		return -1
	}
	if selected < 0 {
		return 0
	}
	return min(max(selected+delta, 0), n-1)
}

// Run renders the dashboard until ctx is cancelled or the user quits. When
// stdin or stdout is not a terminal (or Once is set) it prints a single frame
// and returns.
func (d *Dashboard) Run(ctx context.Context) error {
	interactive := term.IsTerminal(int(os.Stdin.Fd())) &&
		term.IsTerminal(int(os.Stdout.Fd())) && !d.opts.Once

	snap, err := d.Load()
	if err != nil {
		return err
	}
	if !interactive {
		fmt.Println(render(View{Snapshot: snap, Width: 120, Height: 40}))
		return nil
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("putting terminal in raw mode: %w", err)
	}
	defer func() {
		_ = term.Restore(fd, oldState)
		fmt.Print("\x1b[?25h\x1b[?1049l")
	}()
	fmt.Print("\x1b[?1049h\x1b[?25l")

	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	paused := false
	if d.opts.Server != "" {
		snap.LiveNote = "sse connecting"
	}

	var (
		errText  string
		selected = -1
		selID    string
		detail   *httpapi.RequestLog
	)

	// Selection is tracked by request ID so SSE prepends and polling keep the
	// cursor on the same row.
	syncSelection := func() {
		if selID == "" {
			selected = -1
			return
		}
		selected = -1
		for i, e := range snap.Recent {
			if e.ID == selID {
				selected = i
				return
			}
		}
		selID = ""
	}

	draw := func() {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || w <= 0 || h <= 0 {
			w, h = 120, 40
		}
		snap.Err = errText
		fmt.Print("\x1b[H\x1b[2J")
		// Raw mode clears OPOST, so LF alone leaves the cursor in its column.
		frame := render(View{
			Snapshot: snap,
			Width:    w,
			Height:   h,
			Color:    color,
			Paused:   paused,
			Selected: selected,
			Detail:   detail,
		})
		fmt.Print(strings.ReplaceAll(frame, "\n", "\r\n"))
	}
	reload := func() {
		next, err := d.Load()
		if err != nil {
			errText = err.Error()
			draw()
			return
		}
		errText = ""
		next.Live = snap.Live
		next.LiveNote = snap.LiveNote
		snap = next
		syncSelection()
		draw()
	}
	reload()

	var (
		events   chan httpapi.LogSummary
		statuses chan sseStatus
	)
	if d.opts.Server != "" {
		events = make(chan httpapi.LogSummary, 32)
		statuses = make(chan sseStatus, 8)
		go d.runSSE(ctx, events, statuses)
	}

	keys := make(chan tuiKey, 32)
	go func() {
		buf := make([]byte, 16)
		for {
			n, err := os.Stdin.Read(buf)
			for _, k := range parseKeys(buf[:n]) {
				select {
				case keys <- k:
				default:
				}
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()

	move := func(delta int) {
		if detail != nil {
			return
		}
		if len(snap.Recent) == 0 {
			return
		}
		selected = moveSelection(selected, delta, len(snap.Recent))
		selID = snap.Recent[selected].ID
		draw()
	}

	openDetail := func() {
		if selected < 0 || selected >= len(snap.Recent) {
			return
		}
		id := snap.Recent[selected].ID
		entry, err := d.RequestLog(id)
		if err != nil {
			errText = err.Error()
			draw()
			return
		}
		if entry == nil {
			errText = "log " + id + " not found"
			draw()
			return
		}
		errText = ""
		detail = entry
		draw()
	}

	ticker := time.NewTicker(d.opts.Refresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case k, ok := <-keys:
			if !ok {
				return nil
			}
			switch k {
			case 'q', 3:
				return nil
			case keyUp, 'k':
				move(-1)
			case keyDown, 'j':
				move(1)
			case keyEnter:
				if detail != nil {
					detail = nil
					draw()
				} else {
					openDetail()
				}
			case keyEsc:
				if detail != nil {
					detail = nil
				} else {
					selID = ""
					selected = -1
				}
				draw()
			case 'r':
				reload()
			case 'p':
				paused = !paused
				if paused {
					draw()
				} else {
					reload()
				}
			case '1', '2', '3', '4', '5', '6':
				if i := int(k - '1'); i < len(groupBys) && d.SetGroupBy(groupBys[i]) {
					reload()
				}
			}
		case e := <-events:
			snap.Recent = prependRecent(snap.Recent, e)
			snap.Live = true
			snap.LiveNote = "sse live"
			syncSelection()
			draw()
		case st := <-statuses:
			switch {
			case st.connected:
				snap.Live = true
				snap.LiveNote = "sse live"
			case st.err != nil:
				snap.Live = false
				snap.LiveNote = "sse reconnect: " + st.err.Error()
			default:
				snap.Live = false
				snap.LiveNote = "sse connecting"
			}
			draw()
		case <-ticker.C:
			if !paused {
				reload()
			}
		}
	}
}
