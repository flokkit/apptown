package wow

import (
	"regexp"
	"strconv"
	"strings"
)

// summaryLimit bounds a summary in characters: a list row shows a sentence,
// not a page.
const summaryLimit = 200

var (
	summaryImage       = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	summaryLink        = regexp.MustCompile(`\[([^\]]+)\](\([^)]*\)|\[[^\]]*\])`)
	summaryStrong      = regexp.MustCompile(`(\*\*|__)(\S(?:.*?\S)?)(\*\*|__)`)
	summaryStar        = regexp.MustCompile(`\*(\S(?:[^*]*?\S)?)\*`)
	summaryUnder       = regexp.MustCompile(`(^|[^\w])_(\S(?:[^_]*?\S)?)_([^\w]|$)`)
	summaryListItem    = regexp.MustCompile(`^([-*+]|\d+[.)])\s+`)
	summaryRule        = regexp.MustCompile(`^([-=*_])(\s*[-=*_]){2,}$`)
	summaryCode        = regexp.MustCompile("`[^`]+`")
	summaryPlaceholder = regexp.MustCompile("\x00[0-9]+\x00")
)

// PlainSummary is a guidance body's opening prose as one plain sentence for
// a list row. It is made from the markdown while its structure is still
// there, because once whitespace is flattened indented code reads as prose:
// fenced and indented code, HTML comments, tables and headings are skipped;
// emphasis and link syntax are stripped (link text kept); inline code keeps
// its backticks, so the row can set it as code rather than run it into the
// sentence. The first prose paragraph is cut at a word under 200
// characters. A paragraph that ends on a colon introduces what follows: a
// list's items join it ("Go error handling: wrap with context; …"), and
// anything else ends it with a full stop, so a summary never stops on a
// colon. A body with no prose gives its first heading, so a heading-led
// block is never blank.
func PlainSummary(excerpt string) string {
	var para, items []string
	heading, fence, inComment, listing := "", "", false, false
lines:
	for _, line := range strings.Split(strings.ReplaceAll(excerpt, "\r\n", "\n"), "\n") {
		if listing {
			trimmed := strings.TrimSpace(line)
			switch {
			case summaryListItem.MatchString(trimmed):
				items = append(items, summaryListItem.ReplaceAllString(trimmed, ""))
				continue
			case trimmed != "" && len(items) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
				items[len(items)-1] += " " + trimmed
				continue
			case trimmed == "" && len(items) == 0:
				continue
			}
			break lines
		}
		// Code first: indentation is what makes a line code.
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if len(para) > 0 {
				break
			}
			fence = trimmed[:3]
			continue
		}
		if len(para) == 0 && (strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")) {
			continue
		}
		text, open := dropComments(trimmed, inComment)
		wasComment := inComment || text != trimmed
		inComment = open
		text = strings.TrimSpace(text)
		switch {
		case text == "" && wasComment:
			continue // a comment between lines is not a paragraph break
		case text == "" && len(para) > 0 && strings.HasSuffix(para[len(para)-1], ":"):
			// A blank line after a colon: a list may follow.
			listing = true
			continue
		case text == "", strings.HasPrefix(text, "|"):
		case strings.HasPrefix(text, "#"):
			if heading == "" {
				heading = plainInline(strings.TrimRight(strings.TrimLeft(text, "#"), "# "))
			}
		case summaryRule.MatchString(text):
			// A setext underline makes the lines above it a heading.
			if len(para) > 0 && (text[0] == '=' || text[0] == '-') && heading == "" {
				heading = plainInline(strings.Join(para, " "))
			}
			para = nil
			continue
		case summaryListItem.MatchString(text) && len(para) > 0:
			if strings.HasSuffix(para[len(para)-1], ":") {
				listing = true
				items = append(items, summaryListItem.ReplaceAllString(text, ""))
				continue
			}
		default:
			text = strings.TrimSpace(strings.TrimLeft(text, ">"))
			para = append(para, summaryListItem.ReplaceAllString(text, ""))
			continue
		}
		if len(para) > 0 {
			break
		}
	}
	if len(para) == 0 {
		return cutAtWord(heading)
	}
	text := strings.Join(para, " ")
	if strings.HasSuffix(text, ":") {
		if len(items) == 0 {
			text = strings.TrimSuffix(text, ":") + "."
		} else {
			for i, item := range items {
				items[i] = strings.TrimRight(item, ".;")
			}
			text += " " + strings.Join(items, "; ") + "."
		}
	}
	return cutAtWord(plainInline(text))
}

// dropComments removes HTML comments from a line, reporting whether one is
// still open at its end.
func dropComments(line string, inComment bool) (string, bool) {
	var out strings.Builder
	for line != "" {
		if inComment {
			end := strings.Index(line, "-->")
			if end < 0 {
				return out.String(), true
			}
			line, inComment = line[end+3:], false
			continue
		}
		start := strings.Index(line, "<!--")
		if start < 0 {
			out.WriteString(line)
			break
		}
		out.WriteString(line[:start])
		line, inComment = line[start+4:], true
	}
	return out.String(), inComment
}

// plainInline strips inline markdown, keeping the words. Inline code spans
// are kept as they are, backticks included: their text is not markdown.
func plainInline(s string) string {
	// Each code span is set aside under a placeholder while the emphasis
	// around it (which may enclose it: **non-root `USER`**) is stripped.
	var spans []string
	s = summaryCode.ReplaceAllStringFunc(s, func(span string) string {
		spans = append(spans, span)
		return "\x00" + strconv.Itoa(len(spans)-1) + "\x00"
	})
	s = summaryImage.ReplaceAllString(s, "$1")
	s = summaryLink.ReplaceAllString(s, "$1")
	s = summaryStrong.ReplaceAllString(s, "$2")
	s = summaryStar.ReplaceAllString(s, "$1")
	s = summaryUnder.ReplaceAllString(s, "$1$2$3")
	s = summaryPlaceholder.ReplaceAllStringFunc(s, func(mark string) string {
		i, _ := strconv.Atoi(strings.Trim(mark, "\x00"))
		return spans[i]
	})
	return strings.Join(strings.Fields(s), " ")
}

// cutAtWord bounds a summary under summaryLimit characters, at a word.
func cutAtWord(s string) string {
	r := []rune(s)
	if len(r) < summaryLimit {
		return s
	}
	cut := string(r[:summaryLimit-2])
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	// A cut inside a code span ends before the span, so no backtick is left
	// open.
	if strings.Count(cut, "`")%2 == 1 {
		cut = cut[:strings.LastIndex(cut, "`")]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}
