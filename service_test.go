package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSystemdQuoteArg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", "simple"},
		{"", `""`},
		{"/a b/c", `"/a b/c"`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		{"100%", `"100%%"`},
		{"$HOME", `"$$HOME"`},
		{`a "b" \c $d%`, `"a \"b\" \\c $$d%%"`},
	}
	for _, tc := range cases {
		got, err := systemdQuoteArg(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("systemdQuoteArg(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"a\nb", "a\x00b", "ok\xff"} {
		if _, err := systemdQuoteArg(bad); err == nil {
			t.Fatalf("systemdQuoteArg(%q) should be rejected", bad)
		}
	}
}

func TestRenderServiceUnit(t *testing.T) {
	unit, err := renderServiceUnit(
		"/opt/seavx router/seavxrouter",
		"/home/u/project",
		[]string{"-listen", "127.0.0.1:20128", "-api-key", "a b%$"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ExecStart="/opt/seavx router/seavxrouter" -listen 127.0.0.1:20128 -api-key "a b%%$$"`,
		"WorkingDirectory=/home/u/project",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}

	bare, err := renderServiceUnit("/usr/local/bin/seavxrouter", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare, "WorkingDirectory=") {
		t.Fatalf("empty work dir should be omitted:\n%s", bare)
	}
	if !strings.Contains(bare, "ExecStart=/usr/local/bin/seavxrouter\n") {
		t.Fatalf("unexpected ExecStart:\n%s", bare)
	}
}

// stubService replaces the systemctl/journalctl runner and records calls.
func stubService(t *testing.T, calls *[][]string, code int) {
	t.Helper()
	oldLook, oldRun := serviceLookPath, serviceRun
	serviceLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	serviceRun = func(name string, _, _ io.Writer, args ...string) int {
		*calls = append(*calls, append([]string{name}, args...))
		return code
	}
	t.Cleanup(func() { serviceLookPath, serviceRun = oldLook, oldRun })
}

func TestRunServiceInstall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd user units are Linux-specific")
	}
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	var calls [][]string
	stubService(t, &calls, 0)

	var out, errOut bytes.Buffer
	code := runServiceCmd(
		[]string{"install", "--", "-listen", "127.0.0.1:20129", "-api-key", "sk test%"},
		&out,
		&errOut,
	)
	if code != 0 {
		t.Fatalf("install exit = %d, stderr = %s", code, errOut.String())
	}

	wantCalls := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", serviceUnitName},
		{"systemctl", "--user", "restart", serviceUnitName},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("systemctl calls = %v, want %v", calls, wantCalls)
	}

	path := filepath.Join(configDir, "systemd", "user", serviceUnitName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("unit mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(data)
	for _, want := range []string{
		"-listen 127.0.0.1:20129",
		`-api-key "sk test%%"`,
		"WorkingDirectory=",
		"ExecStart=",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	if !strings.Contains(out.String(), "enabled and started "+serviceUnitName) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestRunServiceUninstall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd user units are Linux-specific")
	}
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	path := filepath.Join(configDir, "systemd", "user", serviceUnitName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stub unit"), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	stubService(t, &calls, 0)

	var out, errOut bytes.Buffer
	if code := runServiceCmd([]string{"uninstall"}, &out, &errOut); code != 0 {
		t.Fatalf("uninstall exit = %d, stderr = %s", code, errOut.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit still exists: %v", err)
	}
	wantCalls := [][]string{
		{"systemctl", "--user", "disable", "--now", serviceUnitName},
		{"systemctl", "--user", "daemon-reload"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("systemctl calls = %v, want %v", calls, wantCalls)
	}
	if !strings.Contains(out.String(), "removed") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestRunServiceUninstallKeepsUnitWhenStopFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd user units are Linux-specific")
	}
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	path := filepath.Join(configDir, "systemd", "user", serviceUnitName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stub unit"), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	stubService(t, &calls, 1)

	var out, errOut bytes.Buffer
	if code := runServiceCmd([]string{"uninstall"}, &out, &errOut); code != 1 {
		t.Fatalf("uninstall exit = %d, stderr = %s", code, errOut.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unit should survive a failed stop: %v", err)
	}
	wantCalls := [][]string{
		{"systemctl", "--user", "disable", "--now", serviceUnitName},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("systemctl calls = %v, want %v", calls, wantCalls)
	}
	if !strings.Contains(errOut.String(), "unit left in place") {
		t.Fatalf("stderr = %s", errOut.String())
	}
}

func TestRunServiceNotInstalled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd user units are Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := runServiceCmd([]string{"start"}, &out, &errOut); code != 1 {
		t.Fatalf("start without unit exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "not installed") {
		t.Fatalf("stderr = %s", errOut.String())
	}
}

func TestRunServiceInstallWithoutSystemctl(t *testing.T) {
	oldLook := serviceLookPath
	serviceLookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { serviceLookPath = oldLook })

	var out, errOut bytes.Buffer
	if code := runServiceCmd([]string{"install"}, &out, &errOut); code != 1 {
		t.Fatalf("install without systemctl exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "systemctl not found") {
		t.Fatalf("stderr = %s", errOut.String())
	}
}

func TestRunServiceCmdUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runServiceCmd(nil, &out, &errOut); code != 2 {
		t.Fatalf("no action exit = %d", code)
	}
	if code := runServiceCmd([]string{"-h"}, &out, &errOut); code != 2 {
		t.Fatalf("help exit = %d", code)
	}
	if code := runServiceCmd([]string{"bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown action exit = %d", code)
	}
}
