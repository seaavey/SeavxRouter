package auth

import (
	"os"
	"os/exec"
)

// OpenBrowser launches url in the desktop browser. Set SEAVX_NO_BROWSER=1 to
// keep headless, scripted or test logins from popping a browser tab.
func OpenBrowser(url string) {
	if os.Getenv("SEAVX_NO_BROWSER") != "" {
		return
	}
	if open, err := exec.LookPath("xdg-open"); err == nil {
		exec.Command(open, url).Start()
	}
}
