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
	var errText string

	draw := func() {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || w <= 0 || h <= 0 {
			w, h = 120, 40
		}
		snap.Err = errText
		fmt.Print("\x1b[H\x1b[2J")
		// Raw mode clears OPOST, so LF alone leaves the cursor in its column.
		frame := render(View{Snapshot: snap, Width: w, Height: h, Color: color, Paused: paused})
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

	keys := make(chan byte, 32)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				select {
				case keys <- buf[0]:
				default:
				}
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()

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
			case 'r':
				reload()
			case 'p':
				paused = !paused
				if paused {
					draw()
				} else {
					reload()
				}
			case '1', '2', '3', '4', '5':
				if d.SetGroupBy(groupBys[k-'1']) {
					reload()
				}
			}
		case e := <-events:
			snap.Recent = prependRecent(snap.Recent, e)
			snap.Live = true
			snap.LiveNote = "sse live"
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
