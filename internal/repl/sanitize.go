package repl

import (
	"bytes"
	"io"
	"unicode/utf8"
)

// scanState is the state of the terminal-escape scanner. It is a byte-level
// state machine so it can be resumed across arbitrary Write calls.
type scanState uint8

const (
	stGround    scanState = iota // ordinary text
	stESC                        // consumed ESC, waiting for the sequence introducer
	stESCInter                   // ESC followed by an intermediate byte (charset selector)
	stCSI                        // consumed CSI introducer, collecting the sequence
	stString                     // inside OSC/DCS/APC/PM/SOS; everything is discarded
	stStringESC                  // saw ESC inside a string; ESC \ (or ST) terminates it
)

// maxSeqLen bounds the buffered CSI so a hostile sequence with a million
// parameters cannot grow memory without limit.
const maxSeqLen = 256

// sanitizer holds the resumable scanner state. seq caches a CSI until its final
// byte is known (an escape is never emitted half-finished), and pend caches a
// partial UTF-8 rune at a chunk boundary so valid text is never corrupted.
type sanitizer struct {
	state  scanState
	intro  [4]byte // original CSI introducer: ESC [ or the 8-bit CSI rune
	introN int
	seq    [maxSeqLen]byte
	seqN   int
	csiSGR bool // true only while every parameter byte is 0-9, : or ;
	pend   [utf8.UTFMax]byte
	pendN  int
}

func (s *sanitizer) resetSequence() {
	s.introN = 0
	s.seqN = 0
	s.csiSGR = true
}

// sanitize removes terminal control sequences from untrusted remote output.
// Only SGR (CSI ... m, including 256-colour and RGB colour) is preserved, in
// its original byte form. OSC, DCS, APC, PM, SOS, private-mode/query/cursor/
// erase CSI, charset escapes and stray C0/C1 controls are removed. Truncated
// sequences are dropped and the result is safe for arbitrarily large inputs.
func sanitize(s string) string {
	if s == "" {
		return ""
	}
	var b bytes.Buffer
	b.Grow(len(s))
	st := &sanitizer{}
	_ = st.write(&b, []byte(s)) // bytes.Buffer.Write never fails
	return b.String()
}

// SanitizeWriter returns a writer that sanitises everything written to it and
// forwards the result to w. Scanner state is kept across Write calls, so an
// escape sequence or a UTF-8 rune split over several writes is handled exactly
// as if it had arrived in one piece.
func SanitizeWriter(w io.Writer) io.Writer { return &sanitizeWriter{w: w} }

type sanitizeWriter struct {
	w  io.Writer
	st sanitizer
}

func (sw *sanitizeWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := sw.st.write(sw.w, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// write processes one chunk (plus any cached partial rune) and emits accepted
// bytes to dst. Output is accumulated locally and written once, keeping the
// state machine free of partial-flush bugs.
func (s *sanitizer) write(dst io.Writer, p []byte) error {
	var data []byte
	if s.pendN > 0 {
		data = make([]byte, 0, s.pendN+len(p))
		data = append(data, s.pend[:s.pendN]...)
		data = append(data, p...)
		s.pendN = 0
	} else {
		data = p
	}

	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		b := data[i]
		switch s.state {
		case stGround:
			switch {
			case b == 0x1b: // ESC
				i++
				s.state = stESC
				continue

			case b < 0x20 && b != '\t' && b != '\n' && b != '\r':
				i++ // stray C0 control (BEL, BS, NUL, ...)
				continue

			case b == 0x7f: // DEL
				i++
				continue

			case b >= 0x80:
				r, size := utf8.DecodeRune(data[i:])
				if r == utf8.RuneError && size == 1 {
					// A lone 0x80-0x9f byte is a raw 8-bit C1 control (the
					// single-byte form of ESC sequences). Continuation bytes of
					// valid UTF-8 never reach this branch: DecodeRune consumes a
					// whole rune before we see another byte.
					if b >= 0x80 && b <= 0x9f {
						switch b {
						case 0x9b: // CSI: normalise to ESC [ so output stays valid UTF-8
							s.resetSequence()
							s.intro[0], s.intro[1], s.introN = 0x1b, '[', 2
							s.state = stCSI
						case 0x9d, 0x90, 0x9f, 0x9e: // OSC, DCS, APC, PM
							s.state = stString
						}
						i++
						continue
					}
					// Otherwise: invalid byte, or a rune truncated by the
					// chunk boundary.
					if exp := utf8LeadLen(b); exp > 1 && i+exp > len(data) {
						s.pendN = copy(s.pend[:], data[i:])
						i = len(data)
						break
					}
					i++
					continue
				}
				if r >= 0x80 && r <= 0x9f {
					// 8-bit C1 controls are the single-byte equivalents of the
					// ESC sequences above; map them to the same states.
					switch r { // C1 controls: OSC/DCS/APC/PM and CSI
					case 0x9b: // CSI: normalise to ESC [ so output stays valid UTF-8
						s.resetSequence()
						s.intro[0], s.intro[1], s.introN = 0x1b, '[', 2
						s.state = stCSI
					case 0x9d, 0x90, 0x9f, 0x9e:
						s.state = stString
					}
					i += size
					continue
				}
				out = append(out, data[i:i+size]...) // printable rune
				i += size
				continue

			default:
				out = append(out, b)
				i++
				continue
			}

		case stESC:
			switch {
			case b == '[':
				s.resetSequence()
				s.intro[0], s.intro[1], s.introN = 0x1b, '[', 2
				s.state = stCSI
				i++
			case b == ']' || b == 'P' || b == '_' || b == '^' || b == 'X':
				s.state = stString // OSC, DCS, APC, PM, SOS
				i++
			case b == '\\': // bare ST
				s.state = stGround
				i++
			case b == 0x1b: // ESC ESC: restart
				s.state = stESC
				i++
			case b >= 0x20 && b <= 0x2f: // charset selector
				s.state = stESCInter
				i++
			case b >= 0x30 && b <= 0x7e: // two-byte escape
				s.state = stGround
				i++
			case b >= 0x80: // abort and re-read as UTF-8
				s.state = stGround
				continue
			default: // control byte: drop it and the aborted ESC
				s.state = stGround
				i++
			}

		case stESCInter:
			switch {
			case b >= 0x20 && b <= 0x2f:
				i++
			case b >= 0x30 && b <= 0x7e: // final byte of the escape
				s.state = stGround
				i++
			case b == 0x1b:
				s.state = stESC
				i++
			case b >= 0x80:
				s.state = stGround
				continue
			default:
				s.state = stGround
				i++
			}

		case stCSI:
			switch {
			case b >= 0x30 && b <= 0x3b: // parameter bytes 0-9, ':' and ';'
				s.appendCSI(b)
				i++
			case b >= 0x3c && b <= 0x3f: // private prefix: never SGR
				s.csiSGR = false
				s.appendCSI(b)
				i++
			case b >= 0x20 && b <= 0x2f: // intermediate byte: never SGR
				s.csiSGR = false
				s.appendCSI(b)
				i++
			case b >= 0x40 && b <= 0x7e: // final byte
				if s.csiSGR && b == 'm' {
					out = append(out, s.intro[:s.introN]...)
					out = append(out, s.seq[:s.seqN]...)
					out = append(out, b)
				}
				s.resetSequence()
				s.state = stGround
				i++
			case b == 0x1b: // aborted by a new escape
				s.resetSequence()
				s.state = stESC
				i++
			case b >= 0x80: // abort and re-read as UTF-8
				s.resetSequence()
				s.state = stGround
				continue
			default: // control byte (e.g. newline): drop CSI, keep the byte
				s.resetSequence()
				s.state = stGround
				continue
			}

		case stString:
			switch {
			case b == 0x07, b == 0x9c: // BEL or 8-bit ST terminates the string
				s.state = stGround
				i++
			case b == 0x1b:
				s.state = stStringESC
				i++
			default:
				i++ // every string byte is discarded
			}

		case stStringESC:
			switch {
			case b == '\\' || b == 0x07 || b == 0x9c: // ST / BEL
				s.state = stGround
				i++
			case b == 0x1b:
				i++
			default:
				s.state = stString
				i++
			}
		}
	}
	return writeAll(dst, out)
}

// scanCSI is the stateless CSI parser used by the highlighter's visibleRunes.
// The sanitiser itself uses the resumable state machine above; this helper only
// needs the byte span of a complete CSI in an in-memory highlighted line.
func scanCSI(s string, start int) (end int, keep bool) {
	i := start
	keep = true
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f { // parameter bytes
		if s[i] == '?' || s[i] == '>' || s[i] == '<' || s[i] == '=' {
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

func (s *sanitizer) appendCSI(b byte) {
	if s.seqN < len(s.seq) {
		s.seq[s.seqN] = b
		s.seqN++
	} else {
		s.csiSGR = false // too long: drop the whole sequence
	}
}

// utf8LeadLen returns the expected byte length of a UTF-8 lead byte, or 0 for
// bytes that cannot start a valid rune.
func utf8LeadLen(b byte) int {
	switch {
	case b >= 0xc2 && b <= 0xdf:
		return 2
	case b >= 0xe0 && b <= 0xef:
		return 3
	case b >= 0xf0 && b <= 0xf4:
		return 4
	default:
		return 0
	}
}

func writeAll(dst io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := dst.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
