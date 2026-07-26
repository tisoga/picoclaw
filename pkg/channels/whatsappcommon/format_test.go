package whatsappcommon

import "testing"

func TestFormatMarkdownForWhatsApp(t *testing.T) {
	input := "# Deployment\n**Ready** and *verified*\n~~old~~\n[Open logs](https://example.com/logs)\n`**literal**`"
	want := "*Deployment*\n*Ready* and _verified_\n~old~\nOpen logs (https://example.com/logs)\n`**literal**`"
	if got := FormatMarkdown(input); got != want {
		t.Fatalf("FormatMarkdown() = %q, want %q", got, want)
	}
}

func TestFormatMarkdownPreservesFencedCode(t *testing.T) {
	input := "Before **bold**\n```go\nfmt.Println(\"**literal**\")\n```\nAfter *italic*"
	want := "Before *bold*\n```go\nfmt.Println(\"**literal**\")\n```\nAfter _italic_"
	if got := FormatMarkdown(input); got != want {
		t.Fatalf("FormatMarkdown() = %q, want %q", got, want)
	}
}
