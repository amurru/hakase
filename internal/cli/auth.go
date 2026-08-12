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
	fmt.Fprintf(os.Stderr, "\nRun 'hakase auth <command> -h' for help.\n")
}

func runSetPassword(args []string) int {
	fs := flag.NewFlagSet("auth set-password", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
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

	// Read username
	username, err := readLine("Username: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase auth: failed to read username: %v\n", err)
		return 1
	}
	username = strings.TrimSpace(username)
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
