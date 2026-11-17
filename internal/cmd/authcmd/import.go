package authcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/authextract"
	"github.com/maelvls/slackcli/internal/clictx"
)

// importedWorkspace is one workspace of an import result.
type importedWorkspace struct {
	WorkspaceURL  string `json:"workspace_url"`
	WorkspaceName string `json:"workspace_name"`
}

// runImport extracts credentials with extract and saves them. The reason
// of a failure is always reported: unlike the automatic lookup, the user
// asked for this source explicitly.
func runImport(ctx context.Context, rt *clictx.Context, source, advice string, extract clictx.Extractor) (*authextract.Extracted, []importedWorkspace, error) {
	if extract == nil {
		return nil, nil, fmt.Errorf("importing from %s is not available", source)
	}
	extracted, err := extract(ctx)
	if err == nil && (extracted == nil || len(extracted.Teams) == 0) {
		err = authextract.ErrNotFound
	}
	switch {
	case errors.Is(err, authextract.ErrBraveAppleScriptDisabled), errors.Is(err, authextract.ErrChromeAppleScriptDisabled):
		return nil, nil, fmt.Errorf("%w (macOS asks for your password the first time)", err)
	case errors.Is(err, authextract.ErrNotFound):
		return nil, nil, fmt.Errorf("could not import from %s: %w. %s", source, err, advice)
	case err != nil:
		return nil, nil, fmt.Errorf("could not import from %s: %w", source, err)
	}

	var imported []importedWorkspace
	for _, team := range extracted.Teams {
		// Same filter as Context.Import: only what is actually saved
		// is reported.
		if u, err := auth.NormalizeURL(team.URL); err == nil && team.Token != "" && extracted.CookieD != "" {
			imported = append(imported, importedWorkspace{WorkspaceURL: u, WorkspaceName: team.Name})
		}
	}
	if len(imported) == 0 {
		return nil, nil, fmt.Errorf("could not import from %s: the credentials found are incomplete. %s", source, advice)
	}
	if err := rt.Import(extracted); err != nil {
		return nil, nil, fmt.Errorf("saving the credentials imported from %s: %w", source, err)
	}
	return extracted, imported, nil
}

func printImported(rt *clictx.Context, n int, source string) error {
	_, err := fmt.Fprintf(rt.Stdout, "Imported %d workspace token(s) from %s.\n", n, source)
	return err
}

func newImportDesktop(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "import-desktop",
		Short: "Import xoxc token(s) + d cookie from Slack Desktop data (no need to quit Slack)",
		Long: `Import the xoxc token of every workspace Slack Desktop is signed in to,
plus the shared "d" cookie, from Slack Desktop's local data (macOS, Linux,
Windows). Slack does not need to be quit, and nothing is modified.

On macOS the cookie is decrypted with the "Slack Safe Storage" keychain item:
macOS may ask for permission the first time.

Prints the number of imported workspaces, their URLs and the files read.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			extracted, imported, err := runImport(cmd.Context(), rt, "Slack Desktop",
				"Install Slack Desktop and sign in, or import from a browser instead (auth import-chrome, import-brave, import-firefox).",
				rt.Desktop)
			if err != nil {
				return err
			}
			return rt.Print(struct {
				Imported   int                     `json:"imported"`
				Source     authextract.SourcePaths `json:"source"`
				Workspaces []importedWorkspace     `json:"workspaces"`
			}{len(imported), extracted.Paths, imported})
		},
	}
}

func newImportChrome(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "import-chrome",
		Short: "Import xoxc/xoxd from a logged-in Slack tab in Google Chrome (macOS)",
		Long: `Import xoxc tokens and the "d" cookie from a logged-in Slack tab in Google
Chrome (macOS only). Chrome must be running with Slack open in a tab.

Requires View → Developer → Allow JavaScript from Apple Events to be enabled
in Chrome. The cookie is read from Chrome's cookie database, decrypted with
the "Chrome Safe Storage" keychain item: macOS may ask for permission.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, imported, err := runImport(cmd.Context(), rt, "Chrome",
				"Open Slack in Chrome and make sure you are signed in.", rt.Chrome)
			if err != nil {
				return err
			}
			return printImported(rt, len(imported), "Chrome")
		},
	}
}

func newImportBrave(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "import-brave",
		Short: "Import xoxc/xoxd from a logged-in Slack tab in Brave Browser (macOS)",
		Long: `Import xoxc tokens and the "d" cookie from a logged-in Slack tab in Brave
Browser (macOS only). Brave must be running with Slack open in a tab.

Requires View → Developer → Allow JavaScript from Apple Events to be enabled
in Brave. The cookie is read from Brave's cookie database, decrypted with the
"Brave Safe Storage" keychain item: macOS may ask for permission.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, imported, err := runImport(cmd.Context(), rt, "Brave",
				"Open Slack in Brave and make sure you are signed in.", rt.Brave)
			if err != nil {
				return err
			}
			return printImported(rt, len(imported), "Brave")
		},
	}
}

func newImportFirefox(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "import-firefox",
		Short: "Import xoxc/xoxd from Firefox profile storage (macOS/Linux)",
		Long: `Import xoxc tokens and the "d" cookie from Firefox profile storage (macOS
and Linux). The default profile is tried first, then the others; Firefox does
not need to be quit, and nothing is modified.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, imported, err := runImport(cmd.Context(), rt, "Firefox",
				"Open https://app.slack.com in Firefox and make sure you are signed in.", rt.Firefox)
			if err != nil {
				return err
			}
			return printImported(rt, len(imported), "Firefox")
		},
	}
}

// maxCurlInput bounds what parse-curl reads from standard input.
const maxCurlInput = 4 << 20

func newParseCurl(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "parse-curl",
		Short: "Paste a Slack API request copied as cURL (extracts xoxc/xoxd and saves locally)",
		Long: fmt.Sprintf(`Read a Slack API request copied as cURL from standard input, extract the
xoxc token and the "d" cookie from it, and save them for its workspace.

In the browser's developer tools, open the Network tab on your workspace,
filter on "/api/", right-click a POST request to
https://<workspace>.slack.com/api/… and choose Copy → Copy as cURL. Then:

  pbpaste | %s auth parse-curl

The command is read from standard input so that the credentials never appear
in the process list or the shell history.`, appinfo.Name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxCurlInput+1))
			if err != nil {
				return fmt.Errorf("reading the cURL command from standard input: %w", err)
			}
			if len(raw) > maxCurlInput {
				return fmt.Errorf("the input is larger than %d MiB: paste a single cURL command", maxCurlInput>>20)
			}
			if len(bytes.TrimSpace(raw)) == 0 {
				return errors.New("expected a cURL command on standard input (e.g. pbpaste | " + appinfo.Name + " auth parse-curl)")
			}
			parsed, err := authextract.ParseCurl(string(raw))
			if err != nil {
				return err
			}
			if err := rt.Import(parsed); err != nil {
				return err
			}
			_, err = fmt.Fprintf(rt.Stdout, "Imported tokens for %s.\n", parsed.Teams[0].URL)
			return err
		},
	}
}
