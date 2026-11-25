package builder

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allowed are the strings in the repository that read as a build command
// and may stay: the file, the words (of a Go expression, or the command as a
// script writes it), and why. One that no longer matches anything fails the
// test.
var allowed = []struct{ file, words, why string }{
	{"backend/cmd/toft-agent/dns.go", "link set up", "ip link set … up: a network interface"},
	{"backend/internal/composespec/check.go", "build", "the name of a compose file's build field"},
	{"backend/internal/deployment/service.go", "build", "a stage's name on a deploy's clock"},
	{"backend/internal/deployment/service.go", "compose up", "a stage's name on a deploy's clock"},
	{"backend/internal/docker/orchestrator.go", "build --out --cache-key", "nixpacks build writes a Dockerfile; localBuild builds it, through Run"},
	// The check of a builder on a server builds on the builder it is given
	// (and on Docker's own, to show the difference); it runs by hand.
	{"scripts/build-network-check.sh", "docker buildx build", "the builder's check: Run's own command, and a row's label"},
	{"scripts/build-network-check.sh", `compose -p "bnc$2" build`, "the builder's check: a compose build on the builder"},
	{"scripts/build-network-check.sh", "compose -p bncoom build", "the builder's check: a compose build that runs out of memory"},
	{"scripts/build-network-check.sh", "compose build", "the builder's check: two rows' labels"},
}

// A build, and a compose command that can build, is written in this package
// and nowhere else in the repository's Go and shell: the backend, the agent,
// the installer and every script. Everywhere else it goes through Run, or is
// a compose up that carries --no-build. Nothing is exempt by its name; what
// is allowed is listed above, string by string.
func TestNoBuildCommandIsWrittenOutsideThisPackage(t *testing.T) {
	const root = "../../.."
	used := make([]bool, len(allowed))
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Not the repository's: git's own and tools' folders (worktrees
			// among them), dependencies, the founders' notes.
			name := d.Name()
			if rel == "backend/internal/builder" || name == "node_modules" || name == "private" ||
				strings.HasPrefix(name, ".") && name != "." && name != ".." && name != ".github" {
				return filepath.SkipDir
			}
			return nil
		}
		var got []found
		switch {
		case strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, b := range builds(f) {
				b.line = fset.Position(b.pos).Line
				got = append(got, b)
			}
		case strings.HasSuffix(path, ".sh"):
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			got = shellBuilds(string(src))
		}
	found:
		for _, b := range got {
			for i, a := range allowed {
				if a.file == rel && a.words == b.words {
					used[i] = true
					continue found
				}
			}
			t.Errorf("%s:%d: %s: %q\n\ta build runs through builder.Run, and a compose up that builds nothing carries --no-build",
				rel, b.line, b.why, b.words)
		}
		return nil
	})
	require.NoError(t, err)
	for i, a := range allowed {
		assert.True(t, used[i], "allowed, and no longer there: %s %q", a.file, a.words)
	}
}

// The check itself: what it takes for a build command, and what it lets by.
func TestBuildsFindsACommandHoweverItIsWritten(t *testing.T) {
	for src, want := range map[string]bool{
		`exec.Command("docker", "build", "-t", tag, dir)`:                                  true,
		`exec.Command("docker", "buildx", "bake")`:                                         true,
		`host.Exec(ctx, "docker compose --project-directory "+dir+" up -d")`:               true,
		`host.Exec(ctx, fmt.Sprintf("DOCKER_BUILDKIT=1 docker build -t %s %s", tag, dir))`: true,
		`host.Exec(ctx, "cd "+dir+" && docker compose create")`:                            true,
		`cmd := "docker compose -f docker-compose.yml up"`:                                 true,
		`cmd := "docker --context " + name + " buildx build ."`:                            true,
		`args = append(args, "up", "-d")`:                                                  true,
		`args = append(args, "build")`:                                                     true,
		`run([]string{"compose", "-p", name, "run", "web"})`:                               true,
		`run([]string{"compose", "-p", name, "watch"})`:                                    true,
		`host.Exec(ctx, "docker compose -p "+name+" watch --no-up")`:                       true,
		`retry(func() error { return run("up") }, "--no-build")`:                           true,
		`cli.ImageBuild(ctx, tar, opts)`:                                                   true,
		`args = append(args, "up", "-d", "--no-build")`:                                    false,
		`host.Exec(ctx, "docker compose -p "+name+" up -d --pull never --no-build 2>&1")`:  false,
		`return fmt.Errorf("docker build: %w", err)`:                                       false,
		`log("Running docker compose up... (" + n + " vars)")`:                             false,
		`log.Error("compose up failed", zap.Error(err))`:                                   false,
		`exec.Command("docker", "run", "--rm", image)`:                                     false,
		`exec.Command("docker", "network", "create", name)`:                                false,
		`out = append(out, Change{Field: "build"})`:                                        false,
		`svc := map[string]any{"build": map[string]any{"args": args}}`:                     false,
		`run(Command{Args: []string{"up", "-d"}})`:                                         true,
		`direction := "up"`: false,
		`return errors.New("a compose stack stays in its project: create the workspace's")`: false,
		`pkgs := []string{"docker-buildx-plugin", "docker-compose-plugin"}`:                 false,
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\nfunc f() {\n"+src+"\n}\n", parser.SkipObjectResolution)
		require.NoError(t, err, src)
		assert.Equal(t, want, len(builds(f)) > 0, src)
	}
}

// A script's commands are read a line at a time, with the lines a backslash
// continues; a comment is not a command, and a quoted label is one: a script
// has no sentences to tell from commands.
func TestShellBuildsFindsACommand(t *testing.T) {
	for src, want := range map[string][]string{
		"docker build -t x .":                                        {"docker build"},
		"\tDOCKER_BUILDKIT=1 docker buildx build --load .":           {"docker buildx build"},
		"docker compose -f a.yml \\\n\tup -d":                        {"compose -f a.yml up"},
		"docker compose $files --env-file \"$SECRETS\" up -d":        {`compose $files --env-file "$SECRETS" up`},
		"(cd \"$dir\" && docker compose create)":                     {"compose create"},
		"docker compose up; echo done":                               {"compose up"},
		"docker compose -p x watch":                                  {"compose -p x watch"},
		"docker compose $files up -d --remove-orphans --no-build":    nil,
		"docker compose -p x \\\n\tup -d \\\n\t--no-build":           nil,
		"# docker buildx build runs on the builder":                  nil,
		"docker run --rm \"$image\" true && docker network create n": nil,
	} {
		var got []string
		for _, b := range shellBuilds(src) {
			got = append(got, b.words)
		}
		assert.Equal(t, want, got, src)
	}
	assert.Equal(t, 3, shellBuilds("true\n\ndocker compose \\\n up")[0].line, "the line the command starts on")
}

type found struct {
	pos   token.Pos
	line  int
	words string // the expression's string literals, joined by spaces; a script's command as written
	why   string
}

// shellBuilds returns the build commands written in a shell script.
func shellBuilds(src string) []found {
	var out []found
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		start, line := i+1, lines[i]
		for strings.HasSuffix(line, `\`) && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(line, `\`) + lines[i]
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, c := range commands(line, false) {
			out = append(out, found{line: start, words: strings.Join(strings.Fields(c), " "), why: "a build command"})
		}
	}
	return out
}

// between is what a command has between docker or compose and its verb:
// flags, each with or without a value, and words that start as no word of a
// sentence does.
const between = `(?:\s+-\S+(?:\s+[^-\s]\S*)?|\s+[%/'"$.]\S*)*\s+`

var (
	shellBuild = regexp.MustCompile(`(?:\bdocker` + between + `(?:buildx` + between + `)?|\bbuildx` + between + `)(?:build|bake)\b`)
	shellStack = regexp.MustCompile(`\bcompose` + between + `(?:up|build|create|run|watch)\b`)
	// said is how a sentence goes on after a command's name ("docker
	// build: %w", "compose up failed"); anything else is the command.
	said = regexp.MustCompile(`^(?:[:.;,]|\s+(?:failed|on|of)\b)`)
)

// builds returns the build commands written in f. A command is words of one
// call or list, or pieces of one string, so the string literals of one
// expression are read together; a function literal is its own.
func builds(f *ast.File) []found {
	var out []found
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ImportSpec:
				return false
			case *ast.CallExpr, *ast.CompositeLit, *ast.BinaryExpr, *ast.BasicLit:
				// words: every string literal; args: those that are an
				// argument or an element, where a command's verb is a word
				// of its own (a field's value and a map's key are not).
				var words, args []string
				add := func(l *ast.BasicLit, arg bool) {
					if s, err := strconv.Unquote(l.Value); err == nil && l.Kind == token.STRING {
						words = append(words, s)
						if arg {
							args = append(args, s)
						}
					}
				}
				var read func(ast.Node)
				read = func(n ast.Node) {
					ast.Inspect(n, func(m ast.Node) bool {
						switch m := m.(type) {
						case *ast.FuncLit:
							walk(m.Body)
							return false
						case *ast.KeyValueExpr:
							for _, side := range []ast.Expr{m.Key, m.Value} {
								if l, ok := side.(*ast.BasicLit); ok {
									add(l, false)
								} else {
									read(side)
								}
							}
							return false
						case *ast.SelectorExpr:
							if m.Sel.Name == "ImageBuild" {
								out = append(out, found{pos: m.Pos(), words: "ImageBuild", why: "a build through Docker's API"})
							}
						case *ast.BasicLit:
							add(m, true)
						}
						return true
					})
				}
				read(n)
				if _, lone := n.(*ast.BasicLit); lone {
					args = nil
				} else if _, joined := n.(*ast.BinaryExpr); joined {
					args = nil
				}
				if why := buildIn(words, args); why != "" {
					out = append(out, found{pos: n.Pos(), words: strings.Join(words, " "), why: why})
				}
				return false
			}
			return true
		})
	}
	walk(f)
	return out
}

// buildIn says why words, the string literals of one expression, are a
// build command ("": they aren't). args are those among them that are
// arguments or elements.
func buildIn(words, args []string) string {
	text := strings.Join(words, " ")
	noBuild := strings.Contains(text, "--no-build")
	if len(commands(text, true)) > 0 {
		return "a build command"
	}
	has := func(w string) bool { return slices.Contains(args, w) }
	switch {
	case has("up") && !noBuild:
		return `"up" without --no-build`
	case has("build"), has("bake"):
		return "a build"
	case has("compose") && (has("create") || has("run") || has("watch")):
		return "a compose command that builds"
	}
	return ""
}

// commands returns the build commands in text as they are written: every one,
// but an up where text has --no-build and, in prose (a Go string may be a
// sentence), a command's name that a sentence goes on after.
func commands(text string, prose bool) []string {
	var out []string
	noBuild := strings.Contains(text, "--no-build")
	for _, re := range []*regexp.Regexp{shellBuild, shellStack} {
		for _, m := range re.FindAllStringIndex(text, -1) {
			if prose && said.MatchString(text[m[1]:]) || strings.HasSuffix(text[:m[1]], "up") && noBuild {
				continue
			}
			out = append(out, text[m[0]:m[1]])
		}
	}
	return out
}
