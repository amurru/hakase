// channels.go - the `hakase channels` management CLI. It touches only the
// state file (internal/channel/state), never the channel runtime, so it works
// while the web server is running (cross-process flock keeps writes safe).
//
//	hakase channels status              - show paired users and chat bindings
//	hakase channels pair-code           - print (generating if needed) a pairing code
//	hakase channels revoke <user-id>    - unpair a user (telegram:<id>)
package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"amurru/hakase/internal/channel/state"
)

// RunChannelsCLI implements the channels subcommand.
func RunChannelsCLI(args []string) int {
	if len(args) == 0 {
		channelsUsage()
		return 2
	}
	store, err := state.OpenDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: cannot open channel state: %v\n", err)
		return 1
	}

	switch args[0] {
	case "status":
		return runChannelsStatus(store)
	case "pair-code":
		return runChannelsPairCode(store)
	case "revoke":
		return runChannelsRevoke(store, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hakase: unknown channels subcommand %q\n\n", args[0])
		channelsUsage()
		return 2
	}
}

func channelsUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase channels <subcommand> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  status              show paired users and chat bindings")
	fmt.Fprintln(os.Stderr, "  pair-code           print (generating if needed) a pairing code")
	fmt.Fprintln(os.Stderr, "  revoke <user-id>    unpair a Telegram user")
}

func runChannelsStatus(store *state.Store) int {
	st := store.Get()
	fmt.Printf("Channel state: %s\n", store.Path())
	if len(st.PairedUsers) == 0 {
		fmt.Println("Paired users: none (deny-by-default; enable the Telegram channel in config to start pairing)")
	} else {
		fmt.Printf("Paired users (%d):\n", len(st.PairedUsers))
		for _, u := range st.PairedUsers {
			fmt.Printf("  %s:%d (%s) paired %s\n", u.Channel, u.UserID, u.Username, u.PairedAt.Format(time.RFC3339))
		}
	}
	if pp := st.PendingPairing; pp != nil {
		codeState := "valid"
		if time.Now().After(pp.ExpiresAt) {
			codeState = "expired"
		}
		fmt.Printf("Pending pairing code: %s (%s, expires %s)\n", pp.Code, codeState, pp.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Println("Pending pairing code: none")
	}
	if len(st.Chats) == 0 {
		fmt.Println("Chats: none")
	} else {
		fmt.Printf("Chats (%d):\n", len(st.Chats))
		for key, chat := range st.Chats {
			session := chat.SessionID
			if session == "" {
				session = "-"
			}
			fmt.Printf("  %s session=%s notify=%t\n", key, session, chat.Notify)
		}
	}
	return 0
}

func runChannelsPairCode(store *state.Store) int {
	code, expires, err := state.EnsurePairingCodeWithExpiry(store, 15*time.Minute)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: cannot write channel state: %v\n", err)
		return 1
	}
	fmt.Printf("Pairing code: %s (valid %d minutes)\n", code, int(time.Until(expires).Minutes()+0.5))
	fmt.Println("In Telegram, send this to your bot as: /start " + code)
	return 0
}

func runChannelsRevoke(store *state.Store, args []string) int {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase channels revoke <user-id>")
		return 2
	}
	raw := strings.TrimSpace(fs.Arg(0))
	var channelName string
	userID, err := parseInt64(raw)
	if err != nil {
		// Accept "telegram:12345" as well as a bare ID.
		if name, id, ok := strings.Cut(raw, ":"); ok {
			channelName = name
			userID, err = parseInt64(id)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase: invalid user id %q\n", raw)
			return 2
		}
	}
	removed := 0
	err = store.Update(func(st *state.State) error {
		kept := st.PairedUsers[:0]
		for _, u := range st.PairedUsers {
			match := u.UserID == userID && (channelName == "" || u.Channel == channelName)
			if match {
				removed++
				continue
			}
			kept = append(kept, u)
		}
		st.PairedUsers = kept
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: cannot write channel state: %v\n", err)
		return 1
	}
	if removed == 0 {
		fmt.Printf("No paired user matched %s\n", raw)
		return 1
	}
	fmt.Printf("Revoked %d paired user(s): %s\n", removed, raw)
	return 0
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}
