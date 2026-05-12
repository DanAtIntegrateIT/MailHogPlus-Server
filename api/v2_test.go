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

	gatewayUpper := filterMessagesByFolder(messages, "GATEWAY")
	if len(gatewayUpper) != 2 {
		t.Fatalf("expected 2 gateway messages for uppercase filter, got %d", len(gatewayUpper))
	}
}

func TestFolderResultsCaseInsensitiveMerge(t *testing.T) {
	messages := []data.Message{
		newTestMessage("1", "Gateway"),
		newTestMessage("2", "gateway"),
		newTestMessage("3", "THORLUX"),
		newTestMessage("4", "thorlux"),
		newTestMessage("5", ""),
	}

	results := folderResults(messages, nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 folders, got %d", len(results))
	}

	byLowerName := map[string]folderResult{}
	for _, item := range results {
		byLowerName[normalizeFolder(item.Name)] = item
	}

	gateway, ok := byLowerName["gateway"]
	if !ok {
		t.Fatalf("expected gateway folder result")
	}
	if gateway.Name != "Gateway" {
		t.Fatalf("expected gateway display name to preserve first case Gateway, got %q", gateway.Name)
	}
	if gateway.Count != 2 {
		t.Fatalf("expected gateway count 2, got %d", gateway.Count)
	}

	thorlux, ok := byLowerName["thorlux"]
	if !ok {
		t.Fatalf("expected thorlux folder result")
	}
	if thorlux.Name != "THORLUX" {
		t.Fatalf("expected thorlux display name to preserve first case THORLUX, got %q", thorlux.Name)
	}
	if thorlux.Count != 2 {
		t.Fatalf("expected thorlux count 2, got %d", thorlux.Count)
	}
}

func TestFolderResultsIncludeDefaultFolders(t *testing.T) {
	messages := []data.Message{
		newTestMessage("1", "Support"),
		newTestMessage("2", "support"),
		newTestMessage("3", "qa"),
	}

	defaultFolders := []string{"Sales", "Support", " SALES "}
	results := folderResults(messages, defaultFolders)

	if len(results) != 3 {
		t.Fatalf("expected 3 folders, got %d", len(results))
	}

	byLowerName := map[string]folderResult{}
	for _, item := range results {
		byLowerName[normalizeFolder(item.Name)] = item
	}

	sales, ok := byLowerName["sales"]
	if !ok {
		t.Fatalf("expected sales folder result")
	}
	if sales.Name != "Sales" {
		t.Fatalf("expected sales display name to preserve configured case Sales, got %q", sales.Name)
	}
	if sales.Count != 0 {
		t.Fatalf("expected sales count 0, got %d", sales.Count)
	}

	support, ok := byLowerName["support"]
	if !ok {
		t.Fatalf("expected support folder result")
	}
	if support.Name != "Support" {
		t.Fatalf("expected support display name to preserve configured case Support, got %q", support.Name)
	}
	if support.Count != 2 {
		t.Fatalf("expected support count 2, got %d", support.Count)
	}

	qa, ok := byLowerName["qa"]
	if !ok {
		t.Fatalf("expected qa folder result")
	}
	if qa.Count != 1 {
		t.Fatalf("expected qa count 1, got %d", qa.Count)
	}
}

func TestSanitizeFolderNames(t *testing.T) {
	in := []string{"  Sales  ", "", "support", "SUPPORT", " QA "}
	got := sanitizeFolderNames(in)

	if len(got) != 3 {
		t.Fatalf("expected 3 sanitized folders, got %d", len(got))
	}
	if got[0] != "Sales" {
		t.Fatalf("expected first folder Sales, got %q", got[0])
	}
	if got[1] != "support" {
		t.Fatalf("expected second folder support, got %q", got[1])
	}
	if got[2] != "QA" {
		t.Fatalf("expected third folder QA, got %q", got[2])
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
