package repl

import "strings"

// sanitize removes terminal control sequences that can hijack or query the
// terminal (OSC, DCS, APC, PM, SOS and private/query CSI) from untrusted remote
// output. SGR colour sequences (CSI ... m) and ordinary CSI control sequences
// are preserved, matching the "raw ANSI passthrough, strip queries" rule.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case 0x1b: // ESC-prefixed control string
			if i+1 >= len(s) {
				i++ // drop dangling ESC
				continue
			}
			switch n := s[i+1]; n {
			case ']': // OSC ... BEL | ST
				i = skipOSC(s, i+2)
			case 'P', '_', '^', 'X': // DCS, APC, PM, SOS ... ST
				i = skipST(s, i+2)
			case '[': // CSI
				end, keep := scanCSI(s, i+2)
				if keep {
					b.WriteString(s[i:end])
				}
				i = end
			default:
				i += 2 // unknown ESC sequence: drop it
			}
		case 0x9d: // 8-bit OSC
			i = skipOSC(s, i+1)
		case 0x90, 0x9f, 0x9e, 0x98: // 8-bit DCS, APC, PM, SOS
			i = skipST(s, i+1)
		case 0x9b: // 8-bit CSI
			end, keep := scanCSI(s, i+1)
			if keep {
				b.WriteString(s[i:end])
			}
			i = end
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipOSC consumes an OSC string terminated by BEL (0x07) or ST (ESC \ / 0x9c).
func skipOSC(s string, start int) int {
	for i := start; i < len(s); i++ {
		switch s[i] {
		case 0x07:
			return i + 1
		case 0x9c:
			return i + 1
		case 0x1b:
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	}
	return len(s)
}

// skipST consumes a DCS/APC/PM/SOS string terminated by ST (ESC \ / 0x9c).
func skipST(s string, start int) int {
	for i := start; i < len(s); i++ {
		if s[i] == 0x9c {
			return i + 1
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2
		}
	}
	return len(s)
}

// scanCSI parses a CSI starting after ESC [ (or the 8-bit CSI byte). It
// returns the index just past the final byte and whether the sequence should
// be kept. Private-parameter prefixes (? > <) mark device queries and are
// dropped; everything else (notably SGR) is kept.
func scanCSI(s string, start int) (end int, keep bool) {
	i := start
	keep = true
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f { // parameter bytes
		if s[i] == '?' || s[i] == '>' || s[i] == '<' {
			keep = false
		}
		i++
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f { // intermediate bytes
		i++
	}
	if i >= len(s) || s[i] < 0x40 || s[i] > 0x7e {
		return len(s), false // malformed: drop the rest
	}
	return i + 1, keep
}
