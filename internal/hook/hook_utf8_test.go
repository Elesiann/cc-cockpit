package hook

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Regression for the byte-vs-rune prompt-truncation bug: the original
// implementation did `prompt[:80]`, which is a byte slice and can split a
// multibyte codepoint mid-sequence, producing invalid UTF-8 in the stored
// LastPromptPreview.
//
// The fix truncates to 80 runes so a multibyte prompt is cut on a
// character boundary and the stored preview is always valid UTF-8.
func TestBuild_UserPromptSubmit_DoesNotCorruptUTF8(t *testing.T) {
	// 100 Japanese characters = 300 bytes. With the buggy byte slice,
	// 80 bytes lands mid-codepoint of the 27th character and the result
	// is invalid UTF-8. The fixed rune-aware truncation produces
	// exactly 80 runes (= 240 bytes for 認証フロー) of valid UTF-8.
	const runeCount = 100
	japanese := strings.Repeat("認証フロー", runeCount/4) // 25 reps * 4 runes = 100 runes, 300 bytes
	got := Build("UserPromptSubmit", "sid", map[string]any{"prompt": japanese})
	p := got["payload"].(map[string]any)
	preview, _ := p["prompt_preview"].(string)

	t.Logf("input:  %d runes, %d bytes", utf8.RuneCountInString(japanese), len(japanese))
	t.Logf("output: %d runes, %d bytes", utf8.RuneCountInString(preview), len(preview))

	if !utf8.ValidString(preview) {
		t.Errorf("prompt_preview is not valid UTF-8 (byte-slice split a codepoint)")
	}
	if got := utf8.RuneCountInString(preview); got > 80 {
		t.Errorf("preview has %d runes; expected at most 80 to match the documented cap", got)
	}
}
