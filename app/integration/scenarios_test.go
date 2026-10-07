//go:build integration

package integration_test

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The scenarios through the built binary: the scenario parent's help, with
// its listing, with nothing reachable, each tour twice in a row against the
// isolated stack, a tour clearing what an interrupted run left, and the
// directories tour with the object store unreachable.

// intent matches a step's heading in a tour's narration, [i/n] and the
// intent sentence.
var intent = regexp.MustCompile(`^\[(\d+)/(\d+)\] `)

// narrated fails t unless out narrates every step of a tour in order: its
// headings number 1 to n, each once, and a heading opens the output.
func narrated(t *testing.T, name, out string) {
	t.Helper()
	var steps []int
	total := 0
	for _, line := range lines(out) {
		m := intent.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		i, _ := strconv.Atoi(m[1])
		total, _ = strconv.Atoi(m[2])
		steps = append(steps, i)
	}
	if total == 0 || len(steps) != total {
		t.Fatalf("scenario %s narrated steps %v of %d:\n%s", name, steps, total, out)
	}
	for i, step := range steps {
		if step != i+1 {
			t.Fatalf("scenario %s narrated steps %v, want 1 to %d in order:\n%s", name, steps, total, out)
		}
	}
	if !intent.MatchString(out) {
		t.Errorf("scenario %s output does not open with its first step's heading:\n%s", name, out)
	}
}

// TestScenarioHelp prints the scenario parent's help, which ends with the
// listing, with the database and the object store both on ports nothing
// listens on: the parent declares no node, so its help builds nothing and
// reads no configuration.
func TestScenarioHelp(t *testing.T) {
	tg := target{env: []string{
		fmt.Sprintf("BLOBFS_DATABASE_PORT=%d", closedPort(t)),
		fmt.Sprintf("BLOBFS_STORAGE_ENDPOINT=http://127.0.0.1:%d/devstoreaccount1", closedPort(t)),
	}}
	out, errOut, code := run(t, tg, "scenario")
	if code != 2 || errOut != "" {
		t.Fatalf("scenario exited %d: %q, want 2 with nothing on stderr", code, errOut)
	}
	want := "" +
		"\n\nScenarios:\n" +
		"  directories   Tour the directory commands on Postgres alone: mkdir, ls, stat, mv, rmdir\n" +
		"                uses files\n" +
		"  files         Tour the object commands on Postgres and the store: put, cat, cp, rm, rm --recursive\n" +
		"                uses files, storage\n"
	if !strings.HasPrefix(out, "Run a narrated scenario") || !strings.HasSuffix(out, want) {
		t.Errorf("scenario help:\n%s\nwant the parent's help ending with:\n%s", out, want)
	}
}

// TestScenarios runs each tour twice in a row in one database and container:
// each narrates every step, works in its own area, and removes it, so the
// second run succeeds as the first did and the root is empty after.
func TestScenarios(t *testing.T) {
	tg := open(t)
	ok(t, tg, "schema", "up")
	for _, name := range []string{"directories", "files"} {
		for run := 1; run <= 2; run++ {
			out := ok(t, tg, "scenario", name)
			narrated(t, name, out)
			if !strings.Contains(out, "Nothing to clear") {
				t.Errorf("scenario %s run %d found a working area left behind:\n%s", name, run, out)
			}
		}
	}
	if got := names(ok(t, tg, "ls", "/")); got != "" {
		t.Errorf("ls / after the tours = %s, want nothing left", got)
	}
	// The owner rows went with their directories, whose removal a row left
	// behind would refuse through its foreign key.
	if got := names(ok(t, tg, "ls", "/", "--unit", "0199c0de-0000-7000-8000-0000000000de")); got != "" {
		t.Errorf("ls / as the tour's unit after the tours = %s, want nothing left", got)
	}
}

// TestScenariosClearWhatAnInterruptedRunLeft leaves each tour's working area
// behind with content in it, as a run stopped partway would, and each tour
// clears it first and succeeds.
func TestScenariosClearWhatAnInterruptedRunLeft(t *testing.T) {
	tg := open(t)
	ok(t, tg, "schema", "up")
	ok(t, tg, "mkdir", "/scenario-directories")
	ok(t, tg, "mkdir", "/scenario-directories/alpha")
	ok(t, tg, "mkdir", "/scenario-directories/alpha/echo")
	ok(t, tg, "mkdir", "/scenario-files")
	ok(t, tg, "mkdir", "/scenario-files/docs")
	put(t, tg, "/scenario-files/docs/hello.txt", "left behind")

	for _, name := range []string{"directories", "files"} {
		out := ok(t, tg, "scenario", name)
		narrated(t, name, out)
		if !strings.Contains(out, "An earlier run stopped before its last step") {
			t.Errorf("scenario %s did not clear the area left behind:\n%s", name, out)
		}
	}
	if got := names(ok(t, tg, "ls", "/")); got != "" {
		t.Errorf("ls / after the tours = %s, want nothing left", got)
	}
}

// TestScenarioDirectoriesWithTheStoreUnreachable runs the tours with the
// object store's endpoint on a port nothing listens on: the directories
// tour declares the files node alone, so it never reaches the store and
// succeeds, while the files tour fails at start, once, naming the store's
// node, before narrating anything.
func TestScenarioDirectoriesWithTheStoreUnreachable(t *testing.T) {
	tg := open(t, fmt.Sprintf("BLOBFS_STORAGE_ENDPOINT=http://127.0.0.1:%d/devstoreaccount1", closedPort(t)))
	ok(t, tg, "schema", "up")

	narrated(t, "directories", ok(t, tg, "scenario", "directories"))

	out, errOut, code := run(t, tg, "scenario", "files")
	if prefix := "blobfs scenario files: store: "; code != 1 || !strings.HasPrefix(errOut, prefix) || strings.Count(errOut, "\n") != 1 {
		t.Errorf("scenario files exited %d: %q, want one line starting %q", code, errOut, prefix)
	}
	if out != "" {
		t.Errorf("scenario files stdout = %q, want nothing narrated", out)
	}
}
