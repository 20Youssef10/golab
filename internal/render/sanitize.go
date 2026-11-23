package render

import (
	"bytes"
	"io"
	"unicode/utf8"
)

// parseState is the state of the terminal-escape scanner. The scanner is a
// byte-level state machine so it can be resumed across arbitrary Write calls.
type parseState uint8

const (
	stGround    parseState = iota // ordinary text
	stESC                         // consumed ESC, waiting for the sequence introducer
	stESCInter                    // ESC followed by one or more intermediate bytes
	stCSI                         // consumed ESC [ (or C1 CSI), collecting parameters
	stString                      // inside OSC/DCS/APC/PM; everything is discarded
	stStringESC                   // saw ESC inside a string; ESC \ terminates it
)

// maxCSIParam bounds the parameter buffer. A hostile sequence with a million
// parameters must not grow memory without limit; once the cap is hit the whole
// sequence is dropped even if it later ends in 'm'.
const maxCSIParam = 128

// sanitizer holds the resumable scanner state. pend caches an incomplete UTF-8
// rune at a chunk boundary so a valid rune is never corrupted by streaming.
type sanitizer struct {
	state    parseState
	csi      [maxCSIParam]byte
	csiN     int
	csiInter bool
	csiDrop  bool
	pend     [4]byte
	pendN    int
}

func (s *sanitizer) resetSequence() {
	s.state = stGround
	s.csiN = 0
	s.csiInter = false
	s.csiDrop = false
}

// Sanitize removes terminal control sequences from s. SGR sequences
// (ESC [ … m, including 256-colour and RGB colour) are preserved. OSC, DCS,
// APC, PM, non-SGR CSI (queries, cursor movement, erase), charset escapes and
// stray C0/C1 controls are removed. Truncated sequences are dropped, and the
// function is safe for arbitrarily large inputs.
func Sanitize(s string) string {
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
// escape sequence split over several writes is handled correctly.
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
// bytes to dst. A sequence is never emitted until its final byte is known,
// which is what makes streaming safe. Output is accumulated in a local buffer
// and written once, keeping the state machine free of partial-flush bugs.
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
					// Invalid byte, or a rune truncated by the chunk boundary.
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
					i += size
					switch r {
					case 0x9b: // CSI
						s.resetSequence()
						s.state = stCSI
					case 0x9d, 0x90, 0x9f, 0x9e: // OSC, DCS, APC, PM
						s.state = stString
					case 0x9c: // ST
						s.state = stGround
					default:
						s.state = stGround
					}
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
				s.state = stCSI
				i++
			case b == ']' || b == 'P' || b == '_' || b == '^':
				s.state = stString
				i++
			case b == '\\': // bare ST
				s.state = stGround
				i++
			case b == 0x1b: // ESC ESC: restart
				s.state = stESC
				i++
			case b >= 0x20 && b <= 0x2f: // charset selector, e.g. ESC ( B
				s.state = stESCInter
				i++
			case b >= 0x30 && b <= 0x7e: // two-byte escape, e.g. ESC c
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
			case b >= 0x30 && b <= 0x3f: // parameter bytes 0-9;:<=>?
				if s.csiN < maxCSIParam && !s.csiDrop {
					s.csi[s.csiN] = b
					s.csiN++
				} else {
					s.csiDrop = true
				}
				i++

			case b >= 0x20 && b <= 0x2f: // intermediate byte: never SGR
				s.csiInter = true
				i++

			case b >= 0x40 && b <= 0x7e: // final byte
				if !s.csiDrop && !s.csiInter && b == 'm' {
					// Rebuild only the SGR sequence: ESC [ params m.
					out = append(out, 0x1b, '[')
					out = append(out, s.csi[:s.csiN]...)
					out = append(out, b)
				}
				s.resetSequence()
				i++

			case b == 0x1b: // aborted by a new escape
				s.resetSequence()
				s.state = stESC
				i++

			case b >= 0x80: // abort and re-read as UTF-8
				s.resetSequence()
				continue

			default: // control byte (e.g. newline): drop CSI, keep the byte
				s.resetSequence()
				continue
			}

		case stString:
			switch {
			case b == 0x07: // BEL terminates OSC
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
			case b == '\\': // ST terminates the string
				s.state = stGround
				i++
			case b == 0x07: // tolerate BEL as a terminator here too
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

// utf8LeadLen returns the expected byte length of a UTF-8 lead byte, or 0 for
// bytes that cannot start a valid rune. 0xC0/0xC1 and 0xF5+ are overlong or
// out of range and are treated as invalid.
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
