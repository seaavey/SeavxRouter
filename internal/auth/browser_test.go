package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLookPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	if _, err := lookPath("xdg-open"); err == nil {
		t.Fatal("lookPath found a missing file")
	}

	path := filepath.Join(dir, "xdg-open")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lookPath("xdg-open"); err == nil {
		t.Fatal("lookPath found a non-executable file")
	}

	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := lookPath("xdg-open"); err != nil || got != path {
		t.Fatalf("lookPath = %q, %v; want %q, nil", got, err, path)
	}
}

func TestTermuxOpenURL(t *testing.T) {
	prefix := t.TempDir()
	path := filepath.Join(prefix, "bin", "termux-open-url")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PREFIX", prefix)
	t.Setenv("TERMUX_VERSION", "0.119")
	if got := termuxOpenURL(); got != path {
		t.Fatalf("termuxOpenURL = %q; want %q", got, path)
	}

	t.Setenv("PREFIX", "/opt/not-termux")
	t.Setenv("TERMUX_VERSION", "")
	if got := termuxOpenURL(); got != "" {
		t.Fatalf("termuxOpenURL outside Termux = %q; want empty", got)
	}
}

func TestOpenBrowserUsesTermuxOpener(t *testing.T) {
	prefix := t.TempDir()
	script := filepath.Join(prefix, "bin", "termux-open-url")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$OPEN_OUT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(prefix, "opened")
	t.Setenv("OPEN_OUT", out)
	t.Setenv("PREFIX", prefix)
	t.Setenv("TERMUX_VERSION", "0.119")
	t.Setenv("SEAVX_NO_BROWSER", "")

	OpenBrowser("https://example.test/auth")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(out); err == nil {
			if string(b) != "https://example.test/auth" {
				t.Fatalf("termux-open-url got %q", b)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("termux-open-url was not invoked")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
