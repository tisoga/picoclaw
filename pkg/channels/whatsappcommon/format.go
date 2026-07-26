package whatsappcommon

import (
	"regexp"
	"strings"
)

var (
	markdownHeadingPattern = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.+)$`)
	markdownLinkPattern    = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
)

// FormatMarkdown converts common Markdown emitted by agents and webhook
// clients into WhatsApp's native text formatting syntax.
func FormatMarkdown(text string) string {
	if text == "" {
		return text
	}

	// Preserve fenced code blocks exactly. Formatting markers inside code must
	// remain literal.
	segments := strings.Split(text, "```")
	for i := range segments {
		if i%2 == 1 {
			continue
		}
		segments[i] = formatMarkdownText(segments[i])
	}
	return strings.Join(segments, "```")
}

func formatMarkdownText(text string) string {
	text = markdownLinkPattern.ReplaceAllString(text, `$1 ($2)`)

	var out strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '`' {
			if end := strings.IndexByte(text[i+1:], '`'); end >= 0 {
				end += i + 1
				out.WriteString(text[i : end+1])
				i = end + 1
				continue
			}
		}

		marker, replacement := "", ""
		switch {
		case strings.HasPrefix(text[i:], "**"):
			marker, replacement = "**", "*"
		case strings.HasPrefix(text[i:], "__"):
			marker, replacement = "__", "*"
		case strings.HasPrefix(text[i:], "~~"):
			marker, replacement = "~~", "~"
		case text[i] == '*':
			marker, replacement = "*", "_"
		}
		if marker != "" {
			start := i + len(marker)
			if offset := strings.Index(text[start:], marker); offset > 0 {
				end := start + offset
				out.WriteString(replacement)
				out.WriteString(text[start:end])
				out.WriteString(replacement)
				i = end + len(marker)
				continue
			}
		}

		out.WriteByte(text[i])
		i++
	}
	return markdownHeadingPattern.ReplaceAllString(out.String(), `*$1*`)
}
