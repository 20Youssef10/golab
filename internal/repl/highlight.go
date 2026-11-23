package repl

import (
	"bytes"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// HighlightPython colourises a Python source snippet with chroma using the
// terminal256 formatter, matching the PygmentsLexer(PythonLexer) used by
// upstream's prompt_toolkit session. On any failure the input is returned
// unchanged so editing never depends on a working lexer/style.
func HighlightPython(code string) string {
	lexer := lexers.Get("python")
	if lexer == nil {
		return code
	}
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}
	style := styles.Get("native")
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return code
	}
	var out bytes.Buffer
	if err := formatter.Format(&out, style, iterator); err != nil {
		return code
	}
	return out.String()
}

// visibleRunes counts printable runes ignoring SGR/CSI escape sequences, used
// to position the cursor after a highlighted redraw.
func visibleRunes(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end, _ := scanCSI(s, i+2)
			if end <= i {
				end = i + 2
			}
			i = end
			continue
		}
		if s[i] == 0x1b {
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size <= 0 {
			size = 1
		}
		i += size
		n++
	}
	return n
}
