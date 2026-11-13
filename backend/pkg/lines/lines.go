// Package lines reads a command's or a container's output line by line,
// and no line is too long to read.
package lines

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// maxBytes is the longest line delivered whole. It is this high because the
// SSH path carries the agent's typed answers, one JSON document per line,
// and the agent's own log reader allows the same.
const maxBytes = 1 << 20

const cutNote = " … (line cut at 1 MiB)"

// Read calls emit for each line of r, without its line ending, until r
// ends. A line longer than maxBytes is cut there with a note and the rest
// of it dropped, so it never stops what comes after. It returns r's error,
// nil at EOF.
func Read(r io.Reader, emit func(string)) error {
	br := bufio.NewReader(r)
	var line []byte
	for {
		part, more, err := br.ReadLine()
		if err != nil {
			if len(line) > 0 {
				deliver(line, emit)
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(line) <= maxBytes {
			line = append(line, part...)
		}
		if !more {
			deliver(line, emit)
			line = line[:0]
		}
	}
}

func deliver(line []byte, emit func(string)) {
	if len(line) > maxBytes {
		// The cut can land inside a UTF-8 sequence.
		emit(strings.ToValidUTF8(string(line[:maxBytes]), "") + cutNote)
		return
	}
	emit(string(line))
}
