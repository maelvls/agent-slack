package cmdutil

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

// AllowDashText lets the positional arguments of cmd start with a dash, as
// message text often does ("- first item\n- second item" is how a list is
// written): the flag parser would take such an argument for a flag and
// reject it with "unknown shorthand flag: ' '".
//
// It takes over flag parsing, and with it the checks cobra skips for a
// command that parses its own flags: cmd.Args and cmd.RunE, which must
// both be set, then receive the positional arguments as usual. An
// argument is text rather than a flag when it starts with a dash and what
// would be the flag name contains whitespace; anything goes after "--".
func AllowDashText(cmd *cobra.Command) *cobra.Command {
	validate, run := cmd.Args, cmd.RunE
	cmd.DisableFlagParsing = true
	cmd.Args = cobra.ArbitraryArgs
	cmd.RunE = func(cmd *cobra.Command, raw []string) error {
		args, ok, err := parseArgsWithDashText(cmd, raw)
		if !ok {
			return err
		}
		if err := validate(cmd, args); err != nil {
			return err
		}
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return err
		}
		if err := cmd.ValidateFlagGroups(); err != nil {
			return err
		}
		return run(cmd, args)
	}
	return cmd
}

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
