// Package style colors sandboxer's human-facing chatter — the "sandboxer:"
// narration lines on stderr, their warnings and errors. Styling is enabled
// only when the target writer is a terminal AND the environment does not opt
// out (NO_COLOR set, TERM=dumb); captured output — pipes, files, test buffers
// — stays plain, so scripts and golden tests never see escape codes.
package style

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// ANSI SGR codes, named rather than raw at every call site. Exported for
// Wrap, which colors a prebuilt string (a banner, a notice).
const (
	Bold       = "\x1b[1m"
	Red        = "\x1b[31m"
	Yellow     = "\x1b[33m"
	Cyan       = "\x1b[36m"
	BoldRed    = "\x1b[1;31m"
	BoldYellow = "\x1b[1;33m"
	BoldCyan   = "\x1b[1;36m"
	reset      = "\x1b[0m"
)

// envOff reports the environment-level opt-outs. Read on every call — the
// environment is the user's kill switch (and tests flip it), so it must be
// live, not cached from the first write.
func envOff() bool {
	_, noColor := os.LookupEnv("NO_COLOR")
	return noColor || os.Getenv("TERM") == "dumb"
}

// Enabled reports whether styling applies to w: a terminal must sit behind it
// and the environment must not opt out. Writers that are not *os.File
// (buffers, custom tees) are never styled — there is no terminal to probe.
func Enabled(w io.Writer) bool {
	if envOff() {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return fileIsTerminal(f)
}

// Label returns the "sandboxer:" label styled with code for w — plain when w
// is not styled. The label is what every narration line shares, so coloring
// just it (and leaving the message in the terminal's default color) keeps the
// chatter readable instead of turning it into a rainbow.
func Label(w io.Writer, code string) string {
	if !Enabled(w) {
		return "sandboxer:"
	}
	return labelColored(code)
}

// labelColored is the always-colored core, split out so tests can assert the
// escape sequences without a terminal to probe.
func labelColored(code string) string {
	return code + "sandboxer:" + reset
}

// Infof prints an info narration line: the colored label, then the message in
// the terminal's default color.
func Infof(w io.Writer, format string, a ...any) {
	fmt.Fprintln(w, Label(w, BoldCyan)+" "+fmt.Sprintf(format, a...))
}

// Warnf prints a warning line — label and message yellow, the label bold. A
// warning is a "this needs your attention" line (an unusual state, a skipped
// step, a degraded mode), not a failure.
func Warnf(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if !Enabled(w) {
		fmt.Fprintln(w, "sandboxer: "+msg)
		return
	}
	fmt.Fprintln(w, Label(w, BoldYellow)+" "+Yellow+msg+reset)
}

// Errorf prints an error line — label and message red, the label bold.
func Errorf(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if !Enabled(w) {
		fmt.Fprintln(w, "sandboxer: "+msg)
		return
	}
	fmt.Fprintln(w, Label(w, BoldRed)+" "+Red+msg+reset)
}

// Wrap colors a prebuilt string (a banner, a notice built by fmt.Sprintf
// elsewhere) with code for w — unchanged when w is not styled.
func Wrap(w io.Writer, s, code string) string {
	if !Enabled(w) {
		return s
	}
	return code + s + reset
}

// Banner colors each "sandboxer:" label of a multi-line banner string for w,
// leaving the message text plain; the string is unchanged when w is not
// styled. Banners are composed by fmt.Sprintf far from the writer, so they
// cannot call Infof line by line — this is the string-level equivalent.
func Banner(w io.Writer, s string) string {
	if !Enabled(w) {
		return s
	}
	return bannerColored(s)
}

// bannerColored is the always-colored core, split out so tests can assert the
// escape sequences without a terminal to probe.
func bannerColored(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.Replace(line, "sandboxer:", BoldCyan+"sandboxer:"+reset, 1)
	}
	return strings.Join(lines, "\n")
}
