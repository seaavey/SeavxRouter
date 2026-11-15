package auth

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// OpenBrowser launches url in the desktop browser. Set SEAVX_NO_BROWSER=1 to
// keep headless, scripted or test logins from popping a browser tab.
func OpenBrowser(url string) {
	if os.Getenv("SEAVX_NO_BROWSER") != "" {
		return
	}
	if open := termuxOpenURL(); open != "" {
		exec.Command(open, url).Start()
		return
	}
	if open, err := lookPath("xdg-open"); err == nil {
		exec.Command(open, url).Start()
	}
}

// lookPath mirrors exec.LookPath without access(2): Android's seccomp policy
// kills linux/arm64 binaries on faccessat2, which LookPath uses, so a normal
// PATH search crashes inside Termux with SIGSYS.
func lookPath(file string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		path := filepath.Join(dir, file)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

// termuxOpenURL resolves Termux's opener by absolute path for the same reason.
func termuxOpenURL() string {
	prefix := os.Getenv("PREFIX")
	if prefix == "" || (os.Getenv("TERMUX_VERSION") == "" && !strings.Contains(prefix, "com.termux")) {
		return ""
	}
	path := filepath.Join(prefix, "bin", "termux-open-url")
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}
