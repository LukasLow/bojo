// Package skill embeds the agent skill that `bojo skill` prints.
//
// The Markdown file is the single source of truth: the same bytes are shown by
// the CLI and read by a human in the repository. Do not keep a second copy.
package skill

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed bojo.md
var markdown string

// Runtimes are the optional `bojo skill <runtime>` arguments. In v0.1 they all
// produce the same text; the switch exists so runtime specific notes can be
// added later without changing the command line.
var Runtimes = []string{"claude", "codex", "opencode"}

// Text returns the skill for the given runtime. An empty runtime means the
// generic version.
func Text(runtime string) (string, error) {
	if runtime == "" {
		return markdown, nil
	}
	for _, r := range Runtimes {
		if strings.EqualFold(r, runtime) {
			return markdown, nil
		}
	}
	return "", fmt.Errorf("unknown runtime %q (known: %s)", runtime, strings.Join(Runtimes, ", "))
}
