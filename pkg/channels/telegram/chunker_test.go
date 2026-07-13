package telegram

import (
	"reflect"
	"testing"
)

func TestSmartChunkMarkdown(t *testing.T) {
	text := "Hello\n```javascript\nconst a = 1;\nconst b = 2;\n```\nWorld"
	
	chunks := smartChunkMarkdown(text, 1000)
	if len(chunks) != 1 {
		t.Fatalf("Expected 1 chunk, got %d", len(chunks))
	}

	chunks = smartChunkMarkdown(text, 40)
	
	expected := []string{
		"Hello\n```javascript\nconst a = 1;\n```",
		"```javascript\nconst b = 2;\n```\nWorld",
	}

	if !reflect.DeepEqual(chunks, expected) {
		t.Fatalf("Expected %v, got %v", expected, chunks)
	}
}
