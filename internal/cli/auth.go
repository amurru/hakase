// auth.go - the `hakase auth` CLI: manage authentication (set-password).
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"amurru/hakase/internal/auth"
	"amurru/hakase/internal/config"
	"golang.org/x/term"
)

// Env names for unattended first-run provisioning (Tier 4, docs/
// release-distribution/): when hakase web starts without credentials.json
// and both are set, the admin login is provisioned automatically. The
// password travels through the environment, which same-user processes can
// read - the standard container trade-off, documented in the web refusal
// text and the roadmap.
const (
	AdminUsernameEnv = "HAKASE_ADMIN_USERNAME"
	AdminPasswordEnv = "HAKASE_ADMIN_PASSWORD"
)

// RunAuthCLI dispatches the `hakase auth` subcommand tree.
func RunAuthCLI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: hakase auth <command>\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  set-password  Set the admin password\n")
		fmt.Fprintf(os.Stderr, "\nRun 'hakase auth <command> -h' for help.\n")
		return 2
	}

	switch args[0] {
	case "set-password":
		return runSetPassword(args[1:])
	case "-h", "-help", "--help", "help":
		printAuthUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "hakase auth: unknown command %q\n\n", args[0])
		printAuthUsage()
		return 2
	}
}

func printAuthUsage() {
	fmt.Fprintf(os.Stderr, "Usage: hakase auth <command>\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  set-password  Set the admin password\n")
	fmt.Fprintf(os.Stderr, "                flags: --username <name>, --password-stdin\n")
	fmt.Fprintf(os.Stderr, "                (non-interactive mode only creates credentials;")
	fmt.Fprintf(os.Stderr, " overwriting an existing login needs the interactive flow)\n")
	fmt.Fprintf(os.Stderr, "\nRun 'hakase auth <command> -h' for help.\n")
}

func runSetPassword(args []string) int {
	fs := flag.NewFlagSet("auth set-password", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		flagUsername  = fs.String("username", "", "admin username (skips the username prompt)")
		flagPassStdin = fs.Bool("password-stdin", false, "read the new password from stdin (non-interactive; refuses to overwrite an existing login)")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	home := config.HakaseHome()
	if home == "" {
		fmt.Fprintln(os.Stderr, "hakase auth: cannot determine hakase home directory")
		return 1
	}
	credsPath := filepath.Join(home, "credentials.json")

	// Check if credentials file already exists
	existingCreds, existingErr := auth.LoadCredentials(credsPath)
	hasExisting := existingErr == nil && existingCreds != nil

	// Non-interactive mode (--password-stdin): first-run provisioning for
	// unattended boots (containers, images). It refuses to overwrite an
	// existing login because there is no current-password challenge on this
	// path - resetting an admin password without proving the old one is a
	// local-privilege escalation, not a convenience.
	if *flagPassStdin {
		if hasExisting {
			fmt.Fprintf(os.Stderr, "hakase auth: credentials already exist at %s; overwriting requires the interactive flow (which verifies the current password)\n", credsPath)
			return 1
		}
		if strings.TrimSpace(*flagUsername) == "" {
			fmt.Fprintln(os.Stderr, "hakase auth: --password-stdin requires --username")
			return 2
		}
		pass, err := readPasswordStdin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase auth: failed to read password from stdin: %v\n", err)
			return 1
		}
		if len(pass) < 1 {
			fmt.Fprintln(os.Stderr, "hakase auth: password cannot be empty")
			return 2
		}
		if err := auth.SetPassword(credsPath, strings.TrimSpace(*flagUsername), pass); err != nil {
			fmt.Fprintf(os.Stderr, "hakase auth: failed to save credentials: %v\n", err)
			return 1
		}
		fmt.Printf("Password set for user %q.\n", strings.TrimSpace(*flagUsername))
		return 0
	}

	if hasExisting {
		// Prompt for current password before allowing change
		currentPass, err := readPassword("Current password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase auth: failed to read password: %v\n", err)
			return 1
		}
		if !auth.VerifyPassword(existingCreds, currentPass) {
			fmt.Fprintln(os.Stderr, "hakase auth: current password is incorrect")
			return 1
		}
		fmt.Println("Current password verified.")
	}

	// Read username (pre-filled via --username, prompt otherwise)
	username := strings.TrimSpace(*flagUsername)
	if username == "" {
		line, err := readLine("Username: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase auth: failed to read username: %v\n", err)
			return 1
		}
		username = strings.TrimSpace(line)
	}
	if username == "" {
		fmt.Fprintln(os.Stderr, "hakase auth: username cannot be empty")
		return 2
	}

	// Read new password (twice for confirmation)
	newPass, err := readPassword("New password: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase auth: failed to read password: %v\n", err)
		return 1
	}
	if len(newPass) < 1 {
		fmt.Fprintln(os.Stderr, "hakase auth: password cannot be empty")
		return 2
	}

	confirmPass, err := readPassword("Confirm password: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase auth: failed to read password: %v\n", err)
		return 1
	}

	if newPass != confirmPass {
		fmt.Fprintln(os.Stderr, "hakase auth: passwords do not match")
		return 2
	}

	// Save credentials
	if err := auth.SetPassword(credsPath, username, newPass); err != nil {
		fmt.Fprintf(os.Stderr, "hakase auth: failed to save credentials: %v\n", err)
		return 1
	}

	fmt.Printf("Password set for user %q.\n", username)
	return 0
}

// readPasswordStdin reads one line of password material from stdin (a pipe
// in non-interactive use) and trims the trailing newline / carriage return.
func readPasswordStdin() (string, error) {
	line, err := readLine("")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ProvisionAdminFromEnv implements unattended first-run provisioning for
// `hakase web`: with no credentials on disk and both HAKASE_ADMIN_USERNAME
// and HAKASE_ADMIN_PASSWORD set, the admin login is created and the boot
// continues. Returns provisioned=false (and no error) when credentials
// already exist or neither env var is set - callers keep their existing
// refusal path. A half-configured environment (exactly one var) is an
// error: silently ignoring it would defeat the operator's intent.
func ProvisionAdminFromEnv(credsPath string) (bool, error) {
	if _, err := os.Stat(credsPath); err == nil {
		return false, nil
	}
	user := strings.TrimSpace(os.Getenv(AdminUsernameEnv))
	pass := os.Getenv(AdminPasswordEnv)
	if user == "" && pass == "" {
		return false, nil
	}
	if user == "" || pass == "" {
		return false, fmt.Errorf("%s and %s must both be set for unattended admin provisioning", AdminUsernameEnv, AdminPasswordEnv)
	}
	if err := auth.SetPassword(credsPath, user, pass); err != nil {
		return false, err
	}
	return true, nil
}

// readPassword reads a password from stdin without echoing.
// If stdin is not a terminal (e.g., piped input), reads a line normally.
func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)

	if term.IsTerminal(int(os.Stdin.Fd())) {
		pass, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr) // newline after password input
		if err != nil {
			return "", err
		}
		return string(pass), nil
	}

	// Non-interactive: read a line
	return readLine("")
}

// readLine reads a single line from stdin.
func readLine(prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	// Use os.Stdin directly instead of bufio to share the read position
	// across multiple calls (piped/non-interactive input).
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 && buf[0] == '\n' {
			break
		}
		if n > 0 {
			line = append(line, buf[0])
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				break
			}
			return "", err
		}
	}
	return strings.TrimRight(string(line), "\r"), nil
}
