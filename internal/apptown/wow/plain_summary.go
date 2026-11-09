package wow

import (
	"regexp"
	"strings"
)

// summaryLimit bounds a summary in characters: a list row shows a sentence,
// not a page.
const summaryLimit = 200

var (
	summaryImage    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	summaryLink     = regexp.MustCompile(`\[([^\]]+)\](\([^)]*\)|\[[^\]]*\])`)
	summaryStrong   = regexp.MustCompile(`(\*\*|__)(\S(?:.*?\S)?)(\*\*|__)`)
	summaryStar     = regexp.MustCompile(`\*(\S(?:[^*]*?\S)?)\*`)
	summaryUnder    = regexp.MustCompile(`(^|[^\w])_(\S(?:[^_]*?\S)?)_([^\w]|$)`)
	summaryListItem = regexp.MustCompile(`^([-*+]|\d+[.)])\s+`)
	summaryRule     = regexp.MustCompile(`^([-=*_])(\s*[-=*_]){2,}$`)
)

// PlainSummary is a guidance body's opening prose as one plain sentence for
// a list row. It is made from the markdown while its structure is still
// there, because once whitespace is flattened indented code reads as prose:
// fenced and indented code, HTML comments, tables and headings are skipped;
// emphasis, backticks and link syntax are stripped (link text kept); the
// first prose paragraph is cut at a word under 200 characters. A body with
// no prose gives its first heading, so a heading-led block is never blank.
func PlainSummary(excerpt string) string {
	var para []string
	heading, fence, inComment := "", "", false
	for _, line := range strings.Split(strings.ReplaceAll(excerpt, "\r\n", "\n"), "\n") {
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
	return cutAtWord(plainInline(strings.Join(para, " ")))
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

// plainInline strips inline markdown, keeping the words.
func plainInline(s string) string {
	s = summaryImage.ReplaceAllString(s, "$1")
	s = summaryLink.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "`", "")
	s = summaryStrong.ReplaceAllString(s, "$2")
	s = summaryStar.ReplaceAllString(s, "$1")
	s = summaryUnder.ReplaceAllString(s, "$1$2$3")
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
	return strings.TrimRight(cut, " ,;:") + "…"
}
