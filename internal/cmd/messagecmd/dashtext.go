package messagecmd

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

// Message text often starts with a dash ("- first item\n- second item" is
// how a list is written), which the flag parser takes for a flag and
// rejects with "unknown shorthand flag: ' '". Commands that take message
// text therefore set DisableFlagParsing and parse their arguments with
// parseArgsWithDashText.

// isDashText reports whether arg starts with a dash but cannot be a flag,
// because what would be the flag name contains whitespace.
func isDashText(arg string) bool {
	if !strings.HasPrefix(arg, "-") {
		return false
	}
	name, _, _ := strings.Cut(arg, "=")
	return strings.ContainsFunc(name, unicode.IsSpace)
}

// parseArgsWithDashText parses the flags of a command declared with
// DisableFlagParsing, treating arguments for which isDashText holds as
// positional. It returns the positional arguments; ok is false when the
// command should stop because help was requested (and printed).
func parseArgsWithDashText(cmd *cobra.Command, raw []string) (args []string, ok bool, err error) {
	// Dash-text arguments are swapped for placeholders while the flags
	// are parsed. A value following a flag is left alone: the parser
	// accepts anything there.
	protected := make([]string, len(raw))
	texts := map[string]string{}
	terminated, valueNext := false, false
	for i, a := range raw {
		protected[i] = a
		switch {
		case terminated:
			// After "--" everything is positional already.
		case valueNext:
			valueNext = false
		case a == "--":
			terminated = true
		case isDashText(a):
			placeholder := fmt.Sprintf("\x00text-%d\x00", i)
			texts[placeholder], protected[i] = a, placeholder
		case strings.HasPrefix(a, "--") && !strings.Contains(a, "="):
			f := cmd.Flags().Lookup(a[2:])
			valueNext = f != nil && f.NoOptDefVal == ""
		}
	}

	cmd.DisableFlagParsing = false
	if err := cmd.ParseFlags(protected); err != nil {
		return nil, false, cmd.FlagErrorFunc()(cmd, err)
	}
	if help, _ := cmd.Flags().GetBool("help"); help {
		return nil, false, cmd.Help()
	}
	args = cmd.Flags().Args()
	for i, a := range args {
		if text, found := texts[a]; found {
			args[i] = text
		}
	}
	return args, true, nil
}
