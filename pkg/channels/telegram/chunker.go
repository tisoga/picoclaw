package telegram

import (
	"strings"
)

func smartChunkMarkdown(text string, maxLen int) []string {
	if len([]rune(text)) <= maxLen {
		return []string{text}
	}

	var chunks []string
	lines := strings.Split(text, "\n")

	var currentChunk strings.Builder
	var currentLen int
	var inCodeBlock bool
	var codeLang string

	flush := func() {
		if currentChunk.Len() > 0 {
			if inCodeBlock {
				currentChunk.WriteString("\n```")
			}
			chunks = append(chunks, strings.TrimSpace(currentChunk.String()))
			currentChunk.Reset()
			currentLen = 0

			if inCodeBlock {
				currentChunk.WriteString("```" + codeLang)
				currentLen = len([]rune("```" + codeLang))
			}
		}
	}

	for _, line := range lines {
		lineRunes := []rune(line)
		isCodeBlockBoundary := strings.HasPrefix(strings.TrimSpace(line), "```")

		if isCodeBlockBoundary {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				codeLang = strings.TrimSpace(line)[3:]
			} else {
				codeLang = ""
			}
		}

		lineLen := len(lineRunes) + 1 // +1 for newline

		if lineLen > maxLen {
			flush()
			var splitLine strings.Builder
			splitLen := 0
			for _, r := range lineRunes {
				closeCost := 0
				openCost := 0
				if inCodeBlock {
					closeCost = 4
					openCost = len([]rune("```" + codeLang + "\n"))
				}

				if splitLen+1+closeCost >= maxLen {
					if inCodeBlock {
						splitLine.WriteString("\n```")
					}
					chunks = append(chunks, splitLine.String())
					splitLine.Reset()
					if inCodeBlock {
						splitLine.WriteString("```" + codeLang + "\n")
						splitLen = openCost
					} else {
						splitLen = 0
					}
				}
				splitLine.WriteRune(r)
				splitLen++
			}
			if currentChunk.Len() > 0 {
				currentChunk.WriteString("\n")
			}
			currentChunk.WriteString(splitLine.String())
			currentLen += splitLen
			continue
		}

		closeCost := 0
		if inCodeBlock && !isCodeBlockBoundary {
			closeCost = 4
		}

		if currentLen+lineLen+closeCost > maxLen {
			flush()
		}

		if currentChunk.Len() > 0 {
			currentChunk.WriteString("\n")
		}
		currentChunk.WriteString(line)
		currentLen += lineLen
	}

	if currentChunk.Len() > 0 {
		if inCodeBlock {
			currentChunk.WriteString("\n```")
		}
		chunks = append(chunks, strings.TrimSpace(currentChunk.String()))
	}

	return chunks
}
