package provider

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestToAnthropicMessagesConvertsDataURLImageToBase64Source(t *testing.T) {
	_, out := toAnthropicMessages([]Message{{
		Role: "user",
		ContentParts: []ContentPart{
			{Type: "text", Text: "what is this?"},
			{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,aGVs\n bG8="}},
		},
	}})
	if len(out) != 1 {
		t.Fatalf("messages len = %d, want 1", len(out))
	}

	var blocks []map[string]any
	if err := json.Unmarshal(out[0].Content, &blocks); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2", len(blocks))
	}
	want := map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": "image/png",
			"data":       "aGVsbG8=",
		},
	}
	if !reflect.DeepEqual(blocks[1], want) {
		t.Fatalf("image block = %#v, want %#v", blocks[1], want)
	}
}

func TestToAnthropicMessagesKeepsRemoteImageAsURLSource(t *testing.T) {
	_, out := toAnthropicMessages([]Message{{
		Role: "user",
		ContentParts: []ContentPart{{
			Type: "image_url", ImageURL: &ImageURL{URL: "https://example.com/photo.webp"},
		}},
	}})

	var blocks []map[string]any
	if err := json.Unmarshal(out[0].Content, &blocks); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	want := map[string]any{
		"type": "image",
		"source": map[string]any{
			"type": "url",
			"url":  "https://example.com/photo.webp",
		},
	}
	if !reflect.DeepEqual(blocks[0], want) {
		t.Fatalf("image block = %#v, want %#v", blocks[0], want)
	}
}
