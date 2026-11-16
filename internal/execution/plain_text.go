package execution

// PlainText removes terminal escapes and keeps the last carriage-return redraw
// of each line. CRLF becomes LF. Bytes other than terminal controls, including
// invalid UTF-8, pass through. Fragments are independent: an escape split across
// the stream's fragment bound is not reassembled.
func PlainText(input []byte) []byte {
	out := make([]byte, 0, len(input))
	line := 0
	for i := 0; i < len(input); i++ {
		switch input[i] {
		case '\x1b':
			i++
			if i == len(input) {
				break
			}
			if input[i] == '\n' {
				i--
				continue
			}
			switch input[i] {
			case '[': // CSI ends at a final byte.
				for i++; i < len(input); i++ {
					if input[i] == '\n' {
						i--
						break
					}
					if input[i] >= 0x40 && input[i] <= 0x7e {
						break
					}
				}
			case ']': // OSC ends at BEL or ST.
				for i++; i < len(input); i++ {
					if input[i] == '\n' {
						i--
						break
					}
					if input[i] == '\a' {
						break
					}
					if input[i] == '\x1b' && i+1 < len(input) && input[i+1] == '\\' {
						i++
						break
					}
				}
			}
		case '\r':
			if i+1 < len(input) && input[i+1] == '\n' {
				continue
			}
			out = out[:line]
		case '\n':
			out = append(out, '\n')
			line = len(out)
		default:
			out = append(out, input[i])
		}
	}
	return out
}
