// RTL direction detection for chat content.
//
// When the agent answers in an RTL language (Arabic, Hebrew, Persian, Urdu),
// the message should align right; otherwise it stays left. The rule mirrors
// the Unicode bidirectional algorithm's first-strong-character heuristic:
// scan past whitespace, digits, punctuation, and markdown syntax, and let
// the first strongly-directional letter decide. Neutral-only strings fall
// back to 'ltr' (the app default).

// Strongly RTL ranges: Hebrew through NKo, Arabic blocks, Arabic
// presentation forms, the RLM mark, Hanifi Rohingya and Adlam (astral).
const RTL_RE =
  /[\u0591-\u07FF\u08A0-\u08FF\uFB1D-\uFDFD\uFE70-\uFEFC\u200F\u{10D00}-\u{10D39}\u{1E900}-\u{1E94F}]/u

// Strongly LTR letters: Latin, Latin Extended, Greek, Cyrillic, LRM mark.
// (CJK and other neutral scripts are skipped, like digits and punctuation.)
const LTR_RE = /[A-Za-z\u00C0-\u02AF\u0370-\u03FF\u0400-\u04FF\u200E]/u

export function detectDirection(text: string): 'rtl' | 'ltr' {
  for (const ch of text) {
    if (RTL_RE.test(ch)) return 'rtl'
    if (LTR_RE.test(ch)) return 'ltr'
  }
  return 'ltr'
}
