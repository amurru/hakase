// audit.go - the `hakase audit` CLI: export and verify the hash-chained
// audit trail (docs/permissions/ PM-004).
//
//	hakase audit export [--since 24h] [--format jsonl|csv] [--verify] [--dir logs]
//	hakase audit verify [--dir logs]
//
// Export streams entries oldest-first to stdout (metadata-only CSV drops
// command bodies/args/reasons). --verify re-hashes the chain first and
// refuses to export a broken trail. verify reports the chained count.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
)

func auditUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  hakase audit export [--since 24h] [--format jsonl|csv] [--verify] [--hmac-key-file PATH] [--dir logs]")
	fmt.Fprintln(os.Stderr, "  hakase audit verify [--hmac-key-file PATH] [--dir logs]")
}

// RunAuditCLI implements the audit subcommand.
func RunAuditCLI(args []string) int {
	if len(args) == 0 {
		auditUsage()
		return 2
	}
	switch args[0] {
	case "export":
		return runAuditExport(args[1:])
	case "verify":
		return runAuditVerify(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hakase: unknown audit subcommand %q\n\n", args[0])
		auditUsage()
		return 2
	}
}

func auditDirFlag(fs *flag.FlagSet) *string {
	return fs.String("dir", hakaseagent.AuditDir(), "audit log directory")
}

// useHMACKeyFile installs the verify/export HMAC key. It returns -1 to
// continue, otherwise the CLI exit code.
func useHMACKeyFile(path string) int {
	if path == "" {
		return -1
	}
	key, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: cannot read hmac key file: %v\n", err)
		return 1
	}
	hakaseagent.ConfigureAuditHMACKey(key)
	return -1
}

func runAuditVerify(args []string) int {
	fs := flag.NewFlagSet("audit verify", flag.ContinueOnError)
	dir := auditDirFlag(fs)
	hmacKey := fs.String("hmac-key-file", "", "HMAC key file for HMAC-chained logs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := useHMACKeyFile(*hmacKey); code >= 0 {
		return code
	}
	n, err := hakaseagent.VerifyAuditChain(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: audit verify failed: %v\n", err)
		return 1
	}
	fmt.Printf("audit chain OK (%d chained entries)\n", n)
	return 0
}

func runAuditExport(args []string) int {
	fs := flag.NewFlagSet("audit export", flag.ContinueOnError)
	dir := auditDirFlag(fs)
	since := fs.String("since", "", "only entries at/after this age (Go duration, e.g. 24h)")
	format := fs.String("format", "jsonl", "output format: jsonl|csv")
	verify := fs.Bool("verify", false, "verify the hash chain before exporting")
	hmacKey := fs.String("hmac-key-file", "", "HMAC key file for HMAC-chained logs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := useHMACKeyFile(*hmacKey); code >= 0 {
		return code
	}
	if *format != "jsonl" && *format != "csv" {
		fmt.Fprintf(os.Stderr, "hakase: invalid --format %q (want jsonl|csv)\n", *format)
		return 2
	}
	if *verify {
		n, err := hakaseagent.VerifyAuditChain(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase: audit verify failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "audit chain OK (%d chained entries)\n", n)
	}
	var sinceTime time.Time
	if *since != "" {
		d, err := time.ParseDuration(*since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase: invalid --since %q: %v\n", *since, err)
			return 2
		}
		sinceTime = time.Now().Add(-d)
	}
	entries, err := hakaseagent.ReadAuditEntries(*dir, sinceTime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: audit export failed: %v\n", err)
		return 1
	}
	out := os.Stdout
	if *format == "csv" {
		fmt.Fprintln(out, hakaseagent.AuditCSVHeader)
		for _, e := range entries {
			fmt.Fprintln(out, hakaseagent.AuditCSVRow(e))
		}
		return 0
	}
	enc := json.NewEncoder(out)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			fmt.Fprintf(os.Stderr, "hakase: audit export failed: %v\n", err)
			return 1
		}
	}
	return 0
}
