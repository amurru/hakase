package channel

import (
	"strings"
	"testing"
)

func TestChunkTextShort(t *testing.T) {
	if got := ChunkText("hello", 10); len(got) != 1 || got[0] != "hello" {
		t.Errorf("ChunkText short = %#v", got)
	}
	if got := ChunkText("   ", 10); got != nil {
		t.Errorf("ChunkText blank = %#v, want nil", got)
	}
}

func TestChunkTextLineBoundaries(t *testing.T) {
	text := strings.Repeat("line\n", 200) // 1000 chars
	chunks := ChunkText(text, 200)
	if len(chunks) < 4 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > 200 {
			t.Errorf("chunk %d too long: %d", i, len(c))
		}
		if strings.HasPrefix(c, "\n") || strings.HasSuffix(c, "\n") {
			t.Errorf("chunk %d has stray newlines: %q", i, c)
		}
	}
}

func TestChunkTextHardSplit(t *testing.T) {
	text := strings.Repeat("x", 501)
	chunks := ChunkText(text, 200)
	if len(chunks) != 3 || len(chunks[0]) != 200 {
		t.Errorf("hard split = %d chunks", len(chunks))
	}
}

func TestMarkdownToTelegramHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"escapes html", "a < b & c", "a &lt; b &amp; c"},
		{"bold", "**hi** there", "<b>hi</b> there"},
		{"strike", "~~gone~~", "<s>gone</s>"},
		{"inline code", "run `ls -la` now", "run <code>ls -la</code> now"},
		{"code preserves markers", "use `**not bold**` here", "use <code>**not bold**</code> here"},
		{"link", "see [docs](https://example.com/x)", `see <a href="https://example.com/x">docs</a>`},
		{"heading", "## Title", "<b>Title</b>"},
		{"italic", "*soft* emphasis", "<i>soft</i> emphasis"},
	}
	for _, tc := range cases {
		if got := MarkdownToTelegramHTML(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFences(t *testing.T) {
	md := "before\n```go\nfmt.Println(\"<hi>\")\n```\nafter"
	got := MarkdownToTelegramHTML(md)
	want := "before\n<pre>fmt.Println(&#34;&lt;hi&gt;&#34;)\n</pre>\nafter"
	if got != want {
		t.Errorf("fence: got %q, want %q", got, want)
	}
}

func TestChunkReplyBalancesFences(t *testing.T) {
	// A fence split across chunks must produce balanced <pre> tags per chunk.
	md := "intro\n```\n" + strings.Repeat("line of code\n", 600) + "```\noutro"
	chunks := ChunkReply(md)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if got, want := strings.Count(c, "<pre>"), strings.Count(c, "</pre>"); got != want {
			t.Errorf("chunk %d unbalanced: %d open, %d close\n%s", i, got, want, c)
		}
	}
	if !strings.Contains(chunks[0], "<pre>") {
		t.Errorf("first chunk lost the fence opener")
	}
}

func TestChunkReplyEscapesEntity(t *testing.T) {
	chunks := ChunkReply("text with <tag> & **bold**")
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	if strings.Contains(chunks[0], "<tag>") && !strings.Contains(chunks[0], "&lt;tag&gt;") {
		t.Errorf("raw HTML leaked: %q", chunks[0])
	}
}
