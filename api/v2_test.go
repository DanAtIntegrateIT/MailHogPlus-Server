package api

import (
	"testing"

	"github.com/mailhog/data"
)

func TestFolderFromMessage(t *testing.T) {
	msg := data.Message{
		Content: &data.Content{
			Headers: map[string][]string{
				"x-mailhogplus-folder": []string{"gateway"},
			},
		},
	}

	got := folderFromMessage(msg)
	if got != "gateway" {
		t.Fatalf("expected folder gateway, got %q", got)
	}
}

func TestFilterMessagesByFolder(t *testing.T) {
	messages := []data.Message{
		newTestMessage("1", ""),
		newTestMessage("2", "gateway"),
		newTestMessage("3", "thorlux"),
		newTestMessage("4", "gateway"),
	}

	inbox := filterMessagesByFolder(messages, "")
	if len(inbox) != 1 {
		t.Fatalf("expected 1 inbox message, got %d", len(inbox))
	}

	gateway := filterMessagesByFolder(messages, "gateway")
	if len(gateway) != 2 {
		t.Fatalf("expected 2 gateway messages, got %d", len(gateway))
	}
}

func TestPageMessages(t *testing.T) {
	messages := []data.Message{
		newTestMessage("1", ""),
		newTestMessage("2", ""),
		newTestMessage("3", ""),
	}

	page := pageMessages(messages, 1, 2)
	if len(page) != 2 {
		t.Fatalf("expected 2 paged messages, got %d", len(page))
	}
	if string(page[0].ID) != "2" || string(page[1].ID) != "3" {
		t.Fatalf("unexpected page order: %#v", page)
	}

	empty := pageMessages(messages, 10, 2)
	if len(empty) != 0 {
		t.Fatalf("expected empty page, got %d items", len(empty))
	}
}

func newTestMessage(id, folder string) data.Message {
	headers := map[string][]string{}
	if folder != "" {
		headers[folderHeaderName] = []string{folder}
	}
	return data.Message{
		ID: data.MessageID(id),
		Content: &data.Content{
			Headers: headers,
		},
	}
}
