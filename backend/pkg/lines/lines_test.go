package lines

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func read(t *testing.T, in string) []string {
	t.Helper()
	got := make([]string, 0, 4)
	require.NoError(t, Read(strings.NewReader(in), func(l string) { got = append(got, l) }))
	return got
}

func TestReadLines(t *testing.T) {
	assert.Equal(t, []string{"a", "", "b", "c"}, read(t, "a\n\nb\r\nc"), "ends of lines, an empty line, no final newline")
	assert.Equal(t, []string{}, read(t, ""))
}

func TestReadCap(t *testing.T) {
	at := strings.Repeat("x", maxBytes)
	assert.Equal(t, []string{at, "next"}, read(t, at+"\nnext\n"), "a line at the cap is whole")

	got := read(t, at+"tail\nnext\n")
	assert.Equal(t, []string{at + cutNote, "next"}, got, "one over it is cut and the next still arrives")

	got = read(t, at+"tail")
	assert.Equal(t, []string{at + cutNote}, got, "the last line of the output, with no newline")
}

func TestReadCutKeepsTextValid(t *testing.T) {
	got := read(t, strings.Repeat("x", maxBytes-1)+"é and more\n")
	require.Len(t, got, 1)
	assert.True(t, utf8.ValidString(got[0]))
	assert.True(t, strings.HasSuffix(got[0], "x"+cutNote), "the split character goes")
}

func TestReadReturnsTheReadersError(t *testing.T) {
	boom := errors.New("boom")
	var got []string
	err := Read(iotest.ErrReader(boom), func(l string) { got = append(got, l) })
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, got)
}
