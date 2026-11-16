package projectlifecycle

import (
	"fmt"
	"regexp"
	"strings"
)

var seedHeredoc = regexp.MustCompile(`<<-?["']?([a-zA-Z0-9_]+)["']?`)

// pinDockerfile changes operands only. Logical instructions, comments and
// heredoc bodies are kept intact, so a FROM-looking line in a shell program
// cannot become a registry lookup or an edit.
func pinDockerfile(raw []byte, answer func(string) (string, error)) ([]byte, error) {
	lines := strings.SplitAfter(string(raw), "\n")
	aliases := map[string]bool{}
	escape := byte('\\')
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "#") {
			key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(trimmed, "#")), "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "escape") {
				value = strings.TrimSpace(value)
				if value != "\\" && value != "`" {
					return nil, fmt.Errorf("invalid Dockerfile escape directive")
				}
				escape = value[0]
			}
			continue
		}
		start := i
		logical := lines[i]
		// Replace continuations with equal-length whitespace to retain operand offsets.
		flattened := ""
		for {
			line := lines[i]
			trimmed := strings.TrimRight(line, "\r\n \t")
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				flattened += strings.Repeat(" ", len(line))
			} else if len(trimmed) > 0 && trimmed[len(trimmed)-1] == escape {
				flattened += trimmed[:len(trimmed)-1] + strings.Repeat(" ", len(line)-len(trimmed)+1)
			} else {
				flattened += line
				break
			}
			i++
			if i >= len(lines) {
				return nil, fmt.Errorf("unfinished Dockerfile continuation")
			}
			logical += lines[i]
		}
		words := seedWords.FindAllStringIndex(flattened, -1)
		if len(words) == 0 {
			continue
		}
		word := func(n int) string { return flattened[words[n][0]:words[n][1]] }
		if !strings.EqualFold(word(0), "FROM") {
			// Docker heredocs belong to this instruction, not to the next one.
			for _, match := range seedHeredoc.FindAllStringSubmatch(flattened, -1) {
				delimiter := match[1]
				closed := false
				for i++; i < len(lines); i++ {
					if strings.TrimSpace(lines[i]) == delimiter {
						closed = true
						break
					}
				}
				if !closed {
					return nil, fmt.Errorf("unfinished Dockerfile heredoc %s", delimiter)
				}
			}
			continue
		}
		n := 1
		for n < len(words) && strings.HasPrefix(word(n), "--") {
			n++
		}
		if n >= len(words) || len(words) != n+1 && !(len(words) == n+3 && strings.EqualFold(word(n+1), "AS")) {
			return nil, fmt.Errorf("invalid Dockerfile FROM instruction")
		}
		from := word(n)
		if !aliases[strings.ToLower(from)] && !strings.EqualFold(from, "scratch") && !strings.ContainsAny(from, "$@") {
			to, err := answer(from)
			if err != nil {
				return nil, err
			}
			span := words[n]
			logical = logical[:span[0]] + to + logical[span[1]:]
		}
		if n+2 < len(words) {
			aliases[strings.ToLower(word(n+2))] = true
		}
		lines[start] = logical
		for j := start + 1; j <= i; j++ {
			lines[j] = ""
		}
	}
	return []byte(strings.Join(lines, "")), nil
}
