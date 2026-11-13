package rollout

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMigrateArgv(t *testing.T) {
	cases := map[string][]string{
		"/server migrate":                   {"/server", "migrate"},
		"  ./bin/migrate up  ":              {"./bin/migrate", "up"},
		"migrate -path /m -database $DB up": {"sh", "-c", "migrate -path /m -database $DB up"},
		"./a && ./b":                        {"sh", "-c", "./a && ./b"},
		"./a | tee log":                     {"sh", "-c", "./a | tee log"},
		"./a > out.txt":                     {"sh", "-c", "./a > out.txt"},
		`./a "two words"`:                   {"sh", "-c", `./a "two words"`},
		"./a *.sql":                         {"sh", "-c", "./a *.sql"},
		"FOO=1 ./a":                         {"sh", "-c", "FOO=1 ./a"},
		"./a --flag=value":                  {"./a", "--flag=value"},
	}
	for in, want := range cases {
		assert.Equal(t, want, migrateArgv(in), "%q", in)
	}
}
