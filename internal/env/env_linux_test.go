//go:build linux

package env

import (
	"testing"
)

// TestParseOSReleaseContent parses /etc/os-release content variants. The
// function under test is linux-only (env_linux.go), so this test is
// build-tagged linux and absent from other GOOS builds (WIN-009).
func TestParseOSReleaseContent(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		id, ver  string
		codename string
		pretty   string
	}{
		{
			name: "arch",
			content: `NAME="Arch Linux"
PRETTY_NAME="Arch Linux"
ID=arch
BUILD_ID="rolling"
VERSION_ID=""
ANSI_COLOR="38;2;23;147;209"
HOME_URL="https://archlinux.org/"`,
			id: "arch", pretty: "Arch Linux",
		},
		{
			name: "ubuntu with codename and quotes",
			content: `PRETTY_NAME="Ubuntu 24.04.2 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.2 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
HOME_URL="https://www.ubuntu.com/"`,
			id: "ubuntu", ver: "24.04", codename: "noble", pretty: "Ubuntu 24.04.2 LTS",
		},
		{
			name: "comments and blank lines",
			content: `# some comment

ID=debian

VERSION_ID="12"
# another comment
VERSION_CODENAME=bookworm`,
			id: "debian", ver: "12", codename: "bookworm",
		},
		{
			name:    "empty",
			content: "",
		},
		{
			name:    "malformed no equals",
			content: "THIS IS NOT KEY=VALUE\nID=busybox\n",
			id:      "busybox",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ver, codename, pretty := parseOSReleaseContent(tc.content)
			if id != tc.id {
				t.Errorf("id = %q, want %q", id, tc.id)
			}
			if ver != tc.ver {
				t.Errorf("version = %q, want %q", ver, tc.ver)
			}
			if codename != tc.codename {
				t.Errorf("codename = %q, want %q", codename, tc.codename)
			}
			if pretty != tc.pretty {
				t.Errorf("pretty = %q, want %q", pretty, tc.pretty)
			}
		})
	}
}
