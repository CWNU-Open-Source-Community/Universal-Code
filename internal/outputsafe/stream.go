package outputsafe

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

// Stream incrementally removes terminal control traffic while preserving
// printable UTF-8. Keeping parser and UTF-8 state across writes prevents an
// escape sequence or multibyte rune split across pipe reads from leaking into
// model-visible output.
type Stream struct {
	state   parserState
	pending []byte
}

const maxRedactingStreamLineBytes = 64 * 1024

var (
	privateKeyBegin        = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	privateKeyEnd          = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)
	presentationWhitespace = strings.NewReplacer("\r", "", "\t", "    ")
	// These two redact rules allow whitespace (including LF) between the
	// introducer and its value. Keep that prefix with the following line.
	continuedSecretPrefix = regexp.MustCompile(`(?i)\b(?:(Bearer\s+)|([A-Z0-9_]*(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE[_-]?KEY)[A-Z0-9_]*)(\s*)(?:([:=])\s*)?)$`)
)

// RedactingStream combines terminal-control parsing with bounded logical-line
// buffering and secret redaction. It prevents a token or PEM block split
// across pipe reads from being persisted or exposed before it can be
// recognized. An unterminated oversized line is replaced instead of retained.
type RedactingStream struct {
	controls        Stream
	pending         string
	privateKey      bool
	discardOversize bool
	discardTail     string
	discardPrefix   string
	discardPending  bool
}

func (s *RedactingStream) Feed(data []byte) string {
	return s.consume(s.controls.Feed(data), false)
}

func (s *RedactingStream) Flush() string {
	return s.consume(s.controls.Flush(), true)
}

// DiscardPending excludes output that began before a new reader's fence.
// Keep parsing the buffered text: resetting it could lose a split secret or
// private-key marker. Its completed logical line will be suppressed instead.
func (s *RedactingStream) DiscardPending() {
	if s.pending != "" || len(s.controls.pending) != 0 || s.discardOversize {
		s.discardPending = true
	}
}

func (s *RedactingStream) consume(value string, force bool) string {
	s.pending += value
	var output strings.Builder
	for {
		if s.discardOversize {
			newline := strings.IndexByte(s.pending, '\n')
			if newline < 0 {
				s.observeDiscarded(s.pending)
				s.pending = ""
				if force {
					s.discardOversize = false
					s.discardTail = ""
					s.discardPrefix = ""
					s.discardPending = false
				}
				break
			}
			s.observeDiscarded(s.pending[:newline+1])
			s.pending = s.pending[newline+1:]
			if continuedSecretPrefix.MatchString(s.discardPrefix) {
				continue
			}
			s.discardOversize = false
			s.discardTail = ""
			s.discardPrefix = ""
			if !s.discardPending {
				output.WriteByte('\n')
			}
			s.discardPending = false
			continue
		}

		newline := strings.IndexByte(s.pending, '\n')
		for newline >= 0 && continuedSecretPrefix.MatchString(s.pending[:newline+1]) {
			next := strings.IndexByte(s.pending[newline+1:], '\n')
			if next < 0 {
				newline = -1
				break
			}
			newline += next + 1
		}
		if newline >= 0 {
			line := s.pending[:newline+1]
			s.pending = s.pending[newline+1:]
			if len([]byte(line)) > maxRedactingStreamLineBytes {
				s.observeDiscarded(line)
				s.discardTail = ""
				s.discardPrefix = ""
				if !s.discardPending {
					output.WriteString("[REDACTED:oversized-line]\n")
				}
				s.discardPending = false
				continue
			}
			redacted := s.redactLine(line)
			if !s.discardPending {
				output.WriteString(redacted)
			}
			s.discardPending = false
			continue
		}
		if !force {
			if len([]byte(s.pending)) > maxRedactingStreamLineBytes {
				s.observeDiscarded(s.pending)
				s.pending = ""
				s.discardOversize = true
				if !s.discardPending {
					output.WriteString("[REDACTED:oversized-line]")
				}
			}
			break
		}
		if s.pending != "" {
			var redacted string
			if len([]byte(s.pending)) > maxRedactingStreamLineBytes {
				s.observeDiscarded(s.pending)
				s.discardTail = ""
				s.discardPrefix = ""
				redacted = "[REDACTED:oversized-line]"
			} else {
				redacted = s.redactLine(s.pending)
			}
			if !s.discardPending {
				output.WriteString(redacted)
			}
			s.pending = ""
		}
		s.discardPending = false
		break
	}
	return output.String()
}

func (s *RedactingStream) redactLine(value string) string {
	// Recognize PEM boundaries before an assigned-secret rule can replace the
	// beginning of a marker such as PRIVATE_KEY=-----BEGIN RSA PRIVATE KEY-----.
	if s.privateKey {
		if location := privateKeyMarkerLocation(privateKeyEnd, value); location != nil {
			s.privateKey = false
			return s.redactLine(value[location[1]:])
		}
		return ""
	}
	location := privateKeyMarkerLocation(privateKeyBegin, value)
	if location == nil {
		return redactPrintable(value)
	}
	if end := privateKeyMarkerLocation(privateKeyEnd, value[location[1]:]); end != nil {
		return redactPrintable(value[:location[0]]) + "[REDACTED:private-key]" +
			s.redactLine(value[location[1]+end[1]:])
	}
	s.privateKey = true
	suffix := ""
	if strings.HasSuffix(value, "\n") {
		suffix = "\n"
	}
	return redactPrintable(value[:location[0]]) + "[REDACTED:private-key]" + suffix
}

func privateKeyMarkerLocation(pattern *regexp.Regexp, value string) []int {
	location := pattern.FindStringIndex(presentationWhitespace.Replace(value))
	if location == nil {
		return nil
	}
	// Map the printable marker back to the original text so CR/tab remain
	// available to secret rules on the text before and after the PEM block.
	start, printable := -1, 0
	for i := 0; i < len(value); i++ {
		if start < 0 && printable == location[0] {
			start = i
		}
		switch value[i] {
		case '\r':
		case '\t':
			printable += 4
		default:
			printable++
		}
		if printable == location[1] {
			return []int{start, i + 1}
		}
	}
	return nil
}

func redactPrintable(value string) string {
	// CR/tab are significant whitespace in some secret rules. Redact before
	// normalizing them, then also check the resulting printable form.
	return redact.String(presentationWhitespace.Replace(redact.String(value)))
}

func (s *RedactingStream) observeDiscarded(value string) {
	const markerCarryRunes = 96
	// Preserve the meaning of a trailing sensitive prefix without retaining an
	// unbounded variable name or whitespace run. It may continue across LF.
	prefix := s.discardPrefix + value
	match := continuedSecretPrefix.FindStringSubmatch(prefix)
	switch {
	case match == nil:
		runes := []rune(prefix)
		if len(runes) > markerCarryRunes {
			runes = runes[len(runes)-markerCarryRunes:]
		}
		s.discardPrefix = string(runes)
	case match[1] != "":
		s.discardPrefix = "Bearer "
	case match[4] != "":
		s.discardPrefix = "TOKEN="
	case match[3] != "":
		s.discardPrefix = "TOKEN "
	default:
		s.discardPrefix = "TOKEN"
	}
	value = presentationWhitespace.Replace(value)
	combined := s.discardTail + value
	for combined != "" {
		if s.privateKey {
			location := privateKeyEnd.FindStringIndex(combined)
			if location == nil {
				break
			}
			s.privateKey = false
			combined = combined[location[1]:]
			continue
		}
		location := privateKeyBegin.FindStringIndex(combined)
		if location == nil {
			break
		}
		s.privateKey = true
		combined = combined[location[1]:]
	}
	runes := []rune(s.discardTail + value)
	if len(runes) > markerCarryRunes {
		runes = runes[len(runes)-markerCarryRunes:]
	}
	s.discardTail = string(runes)
}

type parserState uint8

const (
	stateText parserState = iota
	stateEscape
	stateCSI
	stateString
	stateStringEscape
)

// Feed sanitizes one output chunk. Secret redaction is intentionally applied
// by the caller after assembling a bounded logical page or complete stream so
// secret-shaped values split across chunks are still recognized.
func (s *Stream) Feed(data []byte) string {
	if len(data) == 0 && len(s.pending) == 0 {
		return ""
	}
	input := make([]byte, 0, len(s.pending)+len(data))
	input = append(input, s.pending...)
	input = append(input, data...)
	s.pending = s.pending[:0]

	var output strings.Builder
	output.Grow(len(input))
	for len(input) > 0 {
		value, size := utf8.DecodeRune(input)
		if value == utf8.RuneError && size == 1 && !utf8.FullRune(input) {
			s.pending = append(s.pending, input...)
			break
		}
		input = input[size:]
		if value == utf8.RuneError && size == 1 {
			value = '\uFFFD'
		}
		s.consume(value, &output)
	}
	return output.String()
}

// Flush completes the stream, replacing an incomplete final UTF-8 sequence
// and discarding an incomplete terminal control sequence.
func (s *Stream) Flush() string {
	if len(s.pending) == 0 {
		return ""
	}
	s.pending = s.pending[:0]
	if s.state != stateText {
		return ""
	}
	return "\uFFFD"
}

func (s *Stream) consume(value rune, output *strings.Builder) {
	switch s.state {
	case stateEscape:
		switch value {
		case '[':
			s.state = stateCSI
		case ']', 'P', 'X', '^', '_':
			s.state = stateString
		default:
			s.state = stateText
		}
		return
	case stateCSI:
		if value >= 0x40 && value <= 0x7e {
			s.state = stateText
		}
		return
	case stateString:
		switch value {
		case '\a', '\u009c':
			s.state = stateText
		case '\x1b':
			s.state = stateStringEscape
		}
		return
	case stateStringEscape:
		if value == '\\' {
			s.state = stateText
		} else if value != '\x1b' {
			s.state = stateString
		}
		return
	}

	switch value {
	case '\x1b':
		s.state = stateEscape
		return
	case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
		s.state = stateString
		return
	case '\u009b':
		s.state = stateCSI
		return
	case '\n', '\r', '\t':
		output.WriteRune(value)
		return
	}
	if unicode.IsControl(value) || unicode.In(value, unicode.Cf) {
		return
	}
	output.WriteRune(value)
}

// Sanitize is the one-shot form used for already bounded output.
func Sanitize(data []byte) string {
	stream := &Stream{}
	return redact.String(stream.Feed(data) + stream.Flush())
}
