// script.go - Unicode-script detection for TTS voice selection
// (docs/telegram-voice/spec.md): Piper voices are monolingual, so the voice
// must match the script the ANSWER is written in — an Arabic answer needs an
// Arabic voice even when the request arrived in English. Detection is
// rune-range counting: cheap, deterministic, and exactly right for picking
// between configured voices.
package speech

// scriptBuckets maps Unicode ranges to a script bucket. Buckets line up
// with Piper voice language codes via scriptByLang.
var scriptBuckets = []struct {
	name   string
	lo, hi rune
}{
	{"arabic", 0x0600, 0x06FF},
	{"arabic", 0x0750, 0x077F},
	{"arabic", 0xFB50, 0xFDFF},
	{"arabic", 0xFE70, 0xFEFF},
	{"hebrew", 0x0590, 0x05FF},
	{"cyrillic", 0x0400, 0x04FF},
	{"greek", 0x0370, 0x03FF},
	{"devanagari", 0x0900, 0x097F},
	{"thai", 0x0E00, 0x0E7F},
	{"hangul", 0xAC00, 0xD7AF},
	{"hangul", 0x1100, 0x11FF},
	{"kana", 0x3040, 0x30FF}, // hiragana + katakana → Japanese
	{"han", 0x4E00, 0x9FFF},  // CJK ideographs → Chinese (kana checked first)
}

// scriptByLang maps a configured voice language code to its dominant
// script. Codes not listed here are Latin-script.
var scriptByLang = map[string]string{
	"ar": "arabic", "fa": "arabic", "ur": "arabic", "ps": "arabic", "ug": "arabic",
	"he": "hebrew", "iw": "hebrew",
	"ru": "cyrillic", "uk": "cyrillic", "bg": "cyrillic", "sr": "cyrillic", "mk": "cyrillic",
	"el": "greek",
	"hi": "devanagari", "mr": "devanagari", "ne": "devanagari",
	"th": "thai",
	"ja": "japanese",
	"zh": "chinese",
	"ko": "korean",
}

// scriptLatin is returned for text with no (or negligible) non-Latin
// content — Latin-script languages are not distinguishable by script.
const scriptLatin = "latin"

// DetectScript returns the dominant non-Latin script of the text, or
// scriptLatin when the text is Latin/empty. Kana beats CJK ideographs
// (Japanese text mixes both); otherwise the most frequent non-Latin bucket
// wins.
func DetectScript(text string) string {
	counts := make(map[string]int, len(scriptBuckets))
	for _, r := range text {
		for _, b := range scriptBuckets {
			if r >= b.lo && r <= b.hi {
				counts[b.name]++
				break
			}
		}
	}
	// Japanese mixes kana with CJK ideographs: kana presence is decisive.
	if counts["kana"] > 0 {
		return "japanese"
	}
	if counts["hangul"] > 0 {
		return "korean"
	}
	best, bestN := "", 0
	for _, name := range []string{"arabic", "hebrew", "cyrillic", "greek", "devanagari", "thai", "chinese"} {
		if counts[name] > bestN {
			best, bestN = name, counts[name]
		}
	}
	if best != "" {
		return best
	}
	return scriptLatin
}
