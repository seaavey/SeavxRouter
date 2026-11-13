package compress

import (
	"fmt"
	"strings"
	"testing"
)

func bigDiffFixture() string {
	var b strings.Builder
	b.WriteString("diff --git a/main.go b/main.go\n")
	b.WriteString("index 1111111..2222222 100644\n")
	b.WriteString("--- a/main.go\n")
	b.WriteString("+++ b/main.go\n")
	b.WriteString("@@ -1,80 +1,82 @@\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, " context before %02d with padding padding\n", i)
	}
	b.WriteString("-old line\n")
	b.WriteString("+new line\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, " trailing context %02d with padding\n", i)
	}
	b.WriteString("diff --git a/other.go b/other.go\n")
	b.WriteString("--- a/other.go\n")
	b.WriteString("+++ b/other.go\n")
	b.WriteString("@@ -1,3 +1,3 @@\n")
	b.WriteString(" keep\n")
	b.WriteString("-bad\n")
	b.WriteString("+good\n")
	return b.String()
}

func TestCompressDiff(t *testing.T) {
	out := compressDiff(bigDiffFixture())
	for _, want := range []string{"diff --git a/main.go b/main.go", "@@ -1,80 +1,82 @@", "-old line", "+new line", contextElideMarker, "+good"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q:\n%s", want, out)
		}
	}
	for _, drop := range []string{"context before 10", "trailing context 10"} {
		if strings.Contains(out, drop) {
			t.Errorf("diff output kept elided context %q", drop)
		}
	}
}

func TestCompressDiffLongLine(t *testing.T) {
	long := "+" + strings.Repeat("x", 1000)
	out := compressDiff("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n" + long + "\n")
	if strings.Contains(out, strings.Repeat("x", 1000)) {
		t.Fatal("long diff line was not truncated")
	}
	if !strings.Contains(out, "[truncated]") {
		t.Fatal("missing truncation marker")
	}
}

func TestCompressStatus(t *testing.T) {
	in := `On branch main
Your branch is up to date with 'origin/main'.

Changes not staged for commit:
  (use "git add <file>..." to update what will be committed)
  (use "git restore <file>..." to discard changes in working directory)
	modified:   internal/a.go
	modified:   internal/b.go

Untracked files:
  (use "git add <file>..." to include in what will be committed)
	newfile.txt
`
	out := compressStatus(in)
	for _, want := range []string{"On branch main", "Changes not staged for commit:", "modified:   internal/a.go", "Untracked files:", "newfile.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `(use "git`) {
		t.Errorf("status output kept hint lines:\n%s", out)
	}
}

func TestCompressLog(t *testing.T) {
	in := `commit 0123456789abcdef0123456789abcdef01234567
Author: Dev <dev@example.com>
Date:   Mon Nov 10 10:00:00 2025 +0700

    fix: thing one

commit abcdef0123456789abcdef0123456789abcdef01
Author: Dev <dev@example.com>
Date:   Tue Nov 11 11:00:00 2025 +0700

    feat: thing two

    body line
`
	out := compressLog(in)
	lines := splitLines(out)
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "0123456789ab") || !strings.Contains(lines[0], "fix: thing one") {
		t.Errorf("bad first log line: %q", lines[0])
	}
	if !strings.Contains(lines[1], "abcdef012345") || !strings.Contains(lines[1], "feat: thing two") {
		t.Errorf("bad second log line: %q", lines[1])
	}
	if strings.Contains(out, "Author:") {
		t.Errorf("log output kept author lines:\n%s", out)
	}
}

func TestCompressGrep(t *testing.T) {
	in := `src/a.go:10:first match
src/a.go:20:second match
src/b.go:5:third match
src/b.go:6:fourth match
--
src/c.go:1:fifth match
`
	out := compressGrep(in)
	for _, want := range []string{"src/a.go (2)", "  10:first match", "src/b.go (2)", "src/c.go (1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("grep output missing %q:\n%s", want, out)
		}
	}
}

func TestCompressGrepCapsPerFile(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&b, "src/a.go:%d:match %d\n", i+1, i)
	}
	out := compressGrep(b.String())
	if !strings.Contains(out, "src/a.go (10)") {
		t.Errorf("want per-file cap:\n%s", out)
	}
	if !strings.Contains(out, "5 matches elided") {
		t.Errorf("want elision marker:\n%s", out)
	}
}

func TestCompressTestAllPass(t *testing.T) {
	in := `=== RUN   TestA
=== RUN   TestB
--- PASS: TestA (0.00s)
--- PASS: TestB (0.00s)
PASS
ok  	example.com/pkg	0.003s
`
	out := compressTest(in)
	if strings.Contains(out, "TestA") || strings.Contains(out, "TestB") {
		t.Errorf("passing tests were kept:\n%s", out)
	}
	if !strings.Contains(out, "PASS") || !strings.Contains(out, "ok  \texample.com/pkg") {
		t.Errorf("summary lines missing:\n%s", out)
	}
}

func TestCompressTestFailures(t *testing.T) {
	in := `=== RUN   TestA
--- FAIL: TestA (0.00s)
    a_test.go:12: got 1, want 2
=== RUN   TestB
--- PASS: TestB (0.00s)
FAIL
FAIL	example.com/pkg	0.003s
`
	out := compressTest(in)
	for _, want := range []string{"--- FAIL: TestA", "a_test.go:12: got 1, want 2", "FAIL\texample.com/pkg"} {
		if !strings.Contains(out, want) {
			t.Errorf("failure output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--- PASS: TestB") {
		t.Errorf("passing test kept:\n%s", out)
	}
}

func TestCompressTestPytest(t *testing.T) {
	in := `tests/test_a.py::test_ok PASSED
tests/test_b.py::test_bad FAILED
E       AssertionError: assert 1 == 2
==== 1 failed, 1 passed in 0.12s ====
`
	out := compressTest(in)
	for _, want := range []string{"FAILED", "AssertionError", "1 failed, 1 passed"} {
		if !strings.Contains(out, want) {
			t.Errorf("pytest output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "test_ok PASSED") {
		t.Errorf("passing pytest line kept:\n%s", out)
	}
}

func TestCompressLint(t *testing.T) {
	in := `main.go:10:5: undefined: foo
main.go:12:2: unused variable x
util.go:3:1: error: bad thing
util.go:9:4: warning: meh
Found 4 errors
`
	out := compressLint(in)
	for _, want := range []string{"main.go (2)", "  10:5: undefined: foo", "util.go (2)", "Found 4 errors"} {
		if !strings.Contains(out, want) {
			t.Errorf("lint output missing %q:\n%s", want, out)
		}
	}
}

func TestMinifyJSON(t *testing.T) {
	in := "{\n  \"a\": 1,\n  \"b\": [\n    true,\n    null\n  ]\n}"
	if got := minifyJSON(in); got != `{"a":1,"b":[true,null]}` {
		t.Errorf("minify = %q", got)
	}
	if got := minifyJSON("{\"a\": 1}\n{\"b\": 2}\n"); got != "{\"a\":1}\n{\"b\":2}" {
		t.Errorf("jsonl = %q", got)
	}
	if got := minifyJSON("{not json}"); got != "" {
		t.Errorf("invalid = %q", got)
	}
}

func TestCompressListing(t *testing.T) {
	var b strings.Builder
	b.WriteString("total 8\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "-rw-r--r-- 1 user group 123 Nov 10 10:00 file-%03d.go\n", i)
	}
	out := compressListing(b.String())
	if !strings.Contains(out, "total 8") {
		t.Errorf("ls summary missing:\n%s", out)
	}
	if !strings.Contains(out, "lines elided") {
		t.Errorf("listing was not capped:\n%s", out)
	}
}

func TestNormalize(t *testing.T) {
	if got := normalize("\x1b[31mred\x1b[0m"); got != "red" {
		t.Errorf("ansi strip = %q", got)
	}
	if got := normalize("progress 10%\rprogress 100%\ndone"); got != "progress 100%\ndone" {
		t.Errorf("cr collapse = %q", got)
	}
	if got := normalize("same\nsame\nsame\nx"); got != "same (×3)\nx" {
		t.Errorf("dedup = %q", got)
	}
	if got := normalize("a\n\n\n\nb"); got != "a\n\nb" {
		t.Errorf("blank collapse = %q", got)
	}
}

func TestCommandFromArgs(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		{`{"command":"git diff"}`, "git diff"},
		{`{"command":["bash","-lc","git diff"]}`, "bash -lc git diff"},
		{`{"cmd":"git status"}`, "git status"},
		{`{"command":[{"command":"git log"}]}`, "git log"},
		{`{}`, ""},
		{`not json`, ""},
	}
	for _, tc := range cases {
		if got := commandFromArgs(tc.args); got != tc.want {
			t.Errorf("commandFromArgs(%s) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestClassifyCommand(t *testing.T) {
	cases := []struct {
		command string
		want    outputKind
	}{
		{"git diff --stat", kindDiff},
		{"cd repo && git status", kindStatus},
		{"git log --oneline -10", kindLog},
		{"git log -p", kindShow},
		{"git show HEAD", kindShow},
		{"rg TODO src", kindGrep},
		{"ls -la", kindList},
		{"go test ./...", kindTest},
		{"cargo test", kindTest},
		{"npm test", kindTest},
		{"pytest -q", kindTest},
		{"go vet ./...", kindLint},
		{"tsc --noEmit", kindLint},
		{"bash -lc \"git diff\"", kindDiff},
		{"echo hello", kindGeneric},
	}
	for _, tc := range cases {
		if got := classifyCommand(tc.command); got != tc.want {
			t.Errorf("classifyCommand(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}
