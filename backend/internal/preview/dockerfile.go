package preview

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

// heredocStart finds a heredoc's delimiter on an instruction line: <<EOF,
// <<-EOF, <<"EOF" or <<'EOF'.
var heredocStart = regexp.MustCompile(`<<-?\s*["']?([A-Za-z_][A-Za-z0-9_]*)["']?`)

// RunFlags returns the flags an untrusted preview's Dockerfile gives its RUN
// steps (D-97): --mount, --network and --security reach past the build (a
// cache mount is shared with every build on the server), so such a
// Dockerfile is refused before it builds. Line continuations are joined,
// comments and heredoc bodies skipped.
func RunFlags(dockerfile []byte) []string {
	var (
		flags   []string
		logical strings.Builder
		heredoc []string
	)
	sc := bufio.NewScanner(bytes.NewReader(dockerfile))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	check := func(line string) {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "RUN") {
			return
		}
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "--") {
				break
			}
			name, _, _ := strings.Cut(strings.TrimPrefix(f, "--"), "=")
			flags = append(flags, "--"+name)
		}
	}
	for sc.Scan() {
		line := sc.Text()
		if len(heredoc) > 0 {
			if strings.TrimSpace(line) == heredoc[0] {
				heredoc = heredoc[1:]
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if logical.Len() == 0 && strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasSuffix(trimmed, "\\") {
			logical.WriteString(strings.TrimSuffix(trimmed, "\\"))
			logical.WriteByte(' ')
			continue
		}
		logical.WriteString(trimmed)
		full := logical.String()
		logical.Reset()
		check(full)
		for _, m := range heredocStart.FindAllStringSubmatch(full, -1) {
			heredoc = append(heredoc, m[1])
		}
	}
	if logical.Len() > 0 {
		check(logical.String())
	}
	return flags
}
