package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const serviceUnitName = "seavxrouter.service"

func serviceUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: seavxrouter service install [server flags...]")
	fmt.Fprintln(w, "       seavxrouter service uninstall")
	fmt.Fprintln(w, "       seavxrouter service start|stop|restart|status")
	fmt.Fprintln(w, "       seavxrouter service logs [journalctl flags...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "install writes a systemd user unit for the current binary and the flags")
	fmt.Fprintln(w, "after install, enables it at login, and (re)starts it now.")
}

// serviceRun executes a service-management command, forwarding output and
// returning its exit code. Tests replace it to avoid touching systemd.
var serviceRun = func(name string, stdout, stderr io.Writer, args ...string) int {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintf(stderr, "%s: %v\n", name, err)
	return 1
}

// serviceLookPath reports whether systemctl is installed. Tests replace it.
var serviceLookPath = exec.LookPath

func runSystemctl(stdout, stderr io.Writer, args ...string) int {
	return serviceRun("systemctl", stdout, stderr, append([]string{"--user"}, args...)...)
}

// serviceUnitPath is the systemd user unit install writes.
func serviceUnitPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the user config directory: %w", err)
	}
	return filepath.Join(dir, "systemd", "user", serviceUnitName), nil
}

// systemdQuoteArg renders one systemd command-line word. systemd applies its
// own quoting and expands $VAR and %specifiers even inside quotes, so literal
// dollars and percents are doubled.
func systemdQuoteArg(arg string) (string, error) {
	if strings.ContainsAny(arg, "\x00\n\r") {
		return "", fmt.Errorf("service arguments cannot contain newlines or NUL bytes")
	}
	if !utf8.ValidString(arg) {
		return "", fmt.Errorf("service arguments must be valid UTF-8")
	}
	if arg != "" && !strings.ContainsAny(arg, " \t\"'\\$%") {
		return arg, nil
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range arg {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '%':
			b.WriteString("%%")
		case '$':
			b.WriteString("$$")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// renderServiceUnit builds the unit file. workDir is preserved so relative
// flag paths (for example -pricing prices.json) keep resolving as typed.
func renderServiceUnit(execPath, workDir string, serverArgs []string) (string, error) {
	exec, err := systemdQuoteArg(execPath)
	if err != nil {
		return "", err
	}
	words := []string{exec}
	for _, arg := range serverArgs {
		q, err := systemdQuoteArg(arg)
		if err != nil {
			return "", err
		}
		words = append(words, q)
	}
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=SeavxRouter AI gateway\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\n")
	if workDir != "" {
		q, err := systemdQuoteArg(workDir)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", q)
	}
	fmt.Fprintf(&b, "ExecStart=%s\nRestart=on-failure\nRestartSec=2\n\n[Install]\nWantedBy=default.target\n", strings.Join(words, " "))
	return b.String(), nil
}

func writeServiceUnit(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+serviceUnitName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func serviceInstall(serverArgs []string, stdout, stderr io.Writer) int {
	if _, err := serviceLookPath("systemctl"); err != nil {
		fmt.Fprintln(stderr, "systemd user services are not available: systemctl not found")
		return 1
	}
	if len(serverArgs) > 0 && serverArgs[0] == "--" {
		serverArgs = serverArgs[1:]
	}
	path, err := serviceUnitPath()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	execPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "cannot locate the running binary: %v\n", err)
		return 1
	}
	workDir, _ := os.Getwd()
	unit, err := renderServiceUnit(execPath, workDir, serverArgs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(stderr, "cannot create %s: %v\n", filepath.Dir(path), err)
		return 1
	}
	if err := writeServiceUnit(path, unit); err != nil {
		fmt.Fprintf(stderr, "cannot write %s: %v\n", path, err)
		return 1
	}
	fmt.Fprintf(stdout, "unit: %s\n", path)
	if code := runSystemctl(stderr, stderr, "daemon-reload"); code != 0 {
		fmt.Fprintln(stderr, "unit written, but systemctl daemon-reload failed")
		return 1
	}
	if code := runSystemctl(stderr, stderr, "enable", serviceUnitName); code != 0 {
		fmt.Fprintln(stderr, "unit written, but systemctl enable failed")
		return 1
	}
	// restart, not enable --now: an already-active unit would otherwise keep
	// running the previous binary and flags.
	if code := runSystemctl(stderr, stderr, "restart", serviceUnitName); code != 0 {
		fmt.Fprintln(stderr, "unit written and enabled, but systemctl restart failed; stop a manually started server on the same port, then: seavxrouter service restart")
		return 1
	}
	fmt.Fprintf(stdout, "enabled and started %s\n", serviceUnitName)
	fmt.Fprintln(stdout, "hint: loginctl enable-linger $USER keeps it running after logout")
	return 0
}

func serviceUninstall(stdout, stderr io.Writer) int {
	path, err := serviceUnitPath()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintln(stderr, "service is not installed")
		return 1
	}
	// Keep the unit when stopping fails: deleting it would orphan a router that
	// still holds the listen port and can no longer be managed through systemd.
	if code := runSystemctl(io.Discard, stderr, "disable", "--now", serviceUnitName); code != 0 {
		fmt.Fprintf(stderr, "cannot stop %s; unit left in place at %s\n", serviceUnitName, path)
		return 1
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintf(stderr, "cannot remove %s: %v\n", path, err)
		return 1
	}
	if code := runSystemctl(io.Discard, stderr, "daemon-reload"); code != 0 {
		return 1
	}
	fmt.Fprintf(stdout, "removed %s\n", path)
	return 0
}

func serviceControl(action string, stdout, stderr io.Writer) int {
	path, err := serviceUnitPath()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintln(stderr, "service is not installed; run: seavxrouter service install")
		return 1
	}
	if code := runSystemctl(stdout, stderr, action, serviceUnitName); code != 0 {
		return 1
	}
	verb := "restarted"
	switch action {
	case "start":
		verb = "started"
	case "stop":
		verb = "stopped"
	}
	fmt.Fprintf(stdout, "%s %s\n", verb, serviceUnitName)
	return 0
}

func serviceStatus(stdout, stderr io.Writer) int {
	path, err := serviceUnitPath()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintln(stderr, "service is not installed; run: seavxrouter service install")
		return 1
	}
	return runSystemctl(stdout, stderr, "status", "--no-pager", serviceUnitName)
}

func serviceLogs(extra []string, stdout, stderr io.Writer) int {
	args := []string{"--user", "-u", serviceUnitName, "-n", "100", "-f"}
	return serviceRun("journalctl", stdout, stderr, append(args, extra...)...)
}

// runServiceCmd manages a systemd user unit so the router runs in the
// background and comes back at login. Flags after install become ExecStart
// arguments, so they are forwarded verbatim rather than re-parsed.
func runServiceCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		serviceUsage(stderr)
		return 2
	}
	action := args[0]
	switch action {
	case "install":
		return serviceInstall(args[1:], stdout, stderr)
	case "uninstall":
		if len(args) > 1 {
			serviceUsage(stderr)
			return 2
		}
		return serviceUninstall(stdout, stderr)
	case "start", "stop", "restart":
		if len(args) > 1 {
			serviceUsage(stderr)
			return 2
		}
		return serviceControl(action, stdout, stderr)
	case "status":
		if len(args) > 1 {
			serviceUsage(stderr)
			return 2
		}
		return serviceStatus(stdout, stderr)
	case "logs":
		return serviceLogs(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown service action %q (want install, uninstall, start, stop, restart, status or logs)\n", action)
		return 2
	}
}
