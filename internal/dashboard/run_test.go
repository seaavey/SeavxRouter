package dashboard

import "testing"

func TestMoveSelectionClamps(t *testing.T) {
	cases := []struct {
		selected, delta, n, want int
	}{
		{-1, 1, 5, 0},
		{-1, -1, 5, 0},
		{0, -1, 5, 0},
		{4, 1, 5, 4},
		{2, 1, 5, 3},
		{2, -1, 5, 1},
		{0, 0, 0, -1},
	}
	for _, c := range cases {
		if got := moveSelection(c.selected, c.delta, c.n); got != c.want {
			t.Errorf("moveSelection(%d, %d, %d) = %d, want %d", c.selected, c.delta, c.n, got, c.want)
		}
	}
}

func TestParseKeys(t *testing.T) {
	cases := []struct {
		in   string
		want []tuiKey
	}{
		{"\x1b[A\x1b[B", []tuiKey{keyUp, keyDown}},
		{"\x1bOA\x1bOB", []tuiKey{keyUp, keyDown}},
		{"\r\n", []tuiKey{keyEnter, keyEnter}},
		{"qj k", []tuiKey{'q', 'j', ' ', 'k'}},
		{"\x1b", []tuiKey{keyEsc}},
		{"", nil},
	}
	for _, c := range cases {
		got := parseKeys([]byte(c.in))
		if len(got) != len(c.want) {
			t.Errorf("parseKeys(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseKeys(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}
