package api

import (
	"testing"

	"github.com/mailhog/MailHog-Server/config"
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

func TestTagFromMessage(t *testing.T) {
	msg := newTestMessageWithTag("1", "gateway", "jon")
	got := tagFromMessage(msg)
	if got != "jon" {
		t.Fatalf("expected tag jon, got %q", got)
	}
}

func TestTagFromMessageMultiTagUsernameFormat(t *testing.T) {
	msg := newTestMessageWithTags("1", "gateway", []string{"jon:legacy:ops"})
	got := tagFromMessage(msg)
	if got != "jon:legacy:ops" {
		t.Fatalf("expected tags jon:legacy:ops, got %q", got)
	}
}

func TestFilterMessagesByTag(t *testing.T) {
	messages := []data.Message{
		newTestMessageWithTag("1", "", ""),
		newTestMessageWithTag("2", "gateway", "jon"),
		newTestMessageWithTag("3", "gateway", "sara"),
		newTestMessageWithTag("4", "thorlux", "jon"),
		newTestMessageWithTag("5", "gateway", "jon:legacy"),
	}

	jon := filterMessagesByTag(messages, "jon")
	if len(jon) != 3 {
		t.Fatalf("expected 3 jon-tagged messages, got %d", len(jon))
	}

	jonUpper := filterMessagesByTag(messages, "JON")
	if len(jonUpper) != 3 {
		t.Fatalf("expected 3 jon-tagged messages for uppercase filter, got %d", len(jonUpper))
	}

	legacy := filterMessagesByTag(messages, "legacy")
	if len(legacy) != 1 {
		t.Fatalf("expected 1 legacy-tagged message, got %d", len(legacy))
	}

	untagged := filterMessagesByTag(messages, "")
	if len(untagged) != 1 {
		t.Fatalf("expected 1 untagged message, got %d", len(untagged))
	}
}

func TestFilterMessagesByLegacyTagHeader(t *testing.T) {
	message := data.Message{
		ID: data.MessageID("1"),
		Content: &data.Content{
			Headers: map[string][]string{
				legacyTagHeaderName: []string{"legacy:finance"},
			},
		},
	}

	legacy := filterMessagesByTag([]data.Message{message}, "legacy")
	if len(legacy) != 1 {
		t.Fatalf("expected 1 legacy-tagged message from legacy header, got %d", len(legacy))
	}

	finance := filterMessagesByTag([]data.Message{message}, "finance")
	if len(finance) != 1 {
		t.Fatalf("expected 1 finance-tagged message from legacy header, got %d", len(finance))
	}
}

func TestFilterMessagesByTagFallbackFromBodyUsername(t *testing.T) {
	messages := []data.Message{
		newTestMessageWithBody("1", "", "SMTP Username: amazon:finance\r\n"),
		newTestMessageWithBody("2", "", "<p><strong>SMTP Username:</strong> amazon:legacy</p>"),
		newTestMessageWithBody("3", "", "SMTP Username: amazon\r\n"),
		newTestMessageWithBody("4", "", "No username line here"),
	}

	finance := filterMessagesByTag(messages, "finance")
	if len(finance) != 1 {
		t.Fatalf("expected 1 finance-tagged message, got %d", len(finance))
	}
	if string(finance[0].ID) != "1" {
		t.Fatalf("expected finance message id 1, got %s", finance[0].ID)
	}

	legacy := filterMessagesByTag(messages, "legacy")
	if len(legacy) != 1 {
		t.Fatalf("expected 1 legacy-tagged message, got %d", len(legacy))
	}
	if string(legacy[0].ID) != "2" {
		t.Fatalf("expected legacy message id 2, got %s", legacy[0].ID)
	}

	untagged := filterMessagesByTag(messages, "")
	if len(untagged) != 2 {
		t.Fatalf("expected 2 untagged messages, got %d", len(untagged))
	}
}

func TestFilterMessagesByFolderAndTag(t *testing.T) {
	messages := []data.Message{
		newTestMessageWithTag("1", "gateway", "jon"),
		newTestMessageWithTag("2", "gateway", "sara"),
		newTestMessageWithTag("3", "thorlux", "jon"),
		newTestMessageWithTag("4", "", "jon"),
	}

	filtered := filterMessagesByFolder(messages, "gateway")
	filtered = filterMessagesByTag(filtered, "jon")

	if len(filtered) != 1 {
		t.Fatalf("expected 1 gateway+jon message, got %d", len(filtered))
	}
	if string(filtered[0].ID) != "1" {
		t.Fatalf("expected message id 1, got %s", filtered[0].ID)
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

func TestNormalizeOutgoingSMTPTestConfig(t *testing.T) {
	valid, err := normalizeOutgoingSMTPTestConfig(config.OutgoingSMTP{
		Host:      "smtp.example.com",
		Port:      "587",
		Mechanism: "plain",
		Username:  "mailer",
		Password:  "secret",
	})
	if err != nil {
		t.Fatalf("expected config to validate, got error: %v", err)
	}
	if valid.Mechanism != "PLAIN" {
		t.Fatalf("expected PLAIN mechanism, got %q", valid.Mechanism)
	}
	if valid.Username != "mailer" {
		t.Fatalf("expected username mailer, got %q", valid.Username)
	}

	noAuth, err := normalizeOutgoingSMTPTestConfig(config.OutgoingSMTP{
		Host:      "smtp.example.com",
		Port:      "25",
		Mechanism: "none",
		Username:  "ignored",
		Password:  "ignored",
	})
	if err != nil {
		t.Fatalf("expected NONE mechanism config to validate, got error: %v", err)
	}
	if noAuth.Mechanism != "NONE" {
		t.Fatalf("expected NONE mechanism, got %q", noAuth.Mechanism)
	}
	if noAuth.Username != "" || noAuth.Password != "" {
		t.Fatalf("expected auth fields to be cleared for NONE mechanism")
	}
}

func TestNormalizeOutgoingSMTPTestConfigValidationErrors(t *testing.T) {
	_, err := normalizeOutgoingSMTPTestConfig(config.OutgoingSMTP{
		Port: "25",
	})
	if err == nil || err.Error() != "smtp host is required" {
		t.Fatalf("expected missing host error, got %v", err)
	}

	_, err = normalizeOutgoingSMTPTestConfig(config.OutgoingSMTP{
		Host: "smtp.example.com",
	})
	if err == nil || err.Error() != "smtp port is required" {
		t.Fatalf("expected missing port error, got %v", err)
	}

	_, err = normalizeOutgoingSMTPTestConfig(config.OutgoingSMTP{
		Host:      "smtp.example.com",
		Port:      "587",
		Mechanism: "plain",
	})
	if err == nil || err.Error() != "smtp username is required for PLAIN authentication" {
		t.Fatalf("expected missing username error, got %v", err)
	}

	_, err = normalizeOutgoingSMTPTestConfig(config.OutgoingSMTP{
		Host:      "smtp.example.com",
		Port:      "587",
		Mechanism: "xoauth2",
	})
	if err == nil || err.Error() != "unsupported smtp authentication mechanism: XOAUTH2" {
		t.Fatalf("expected unsupported mechanism error, got %v", err)
	}
}

func TestIsImplicitTLSSMTPPort(t *testing.T) {
	if !isImplicitTLSSMTPPort("465") {
		t.Fatalf("expected port 465 to use implicit TLS")
	}
	if !isImplicitTLSSMTPPort(" 465 ") {
		t.Fatalf("expected trimmed port 465 to use implicit TLS")
	}
	if isImplicitTLSSMTPPort("587") {
		t.Fatalf("expected port 587 to avoid implicit TLS")
	}
}

func TestEmailQualityInputFromMessage(t *testing.T) {
	message := &data.Message{
		Content: &data.Content{
			Headers: map[string][]string{
				"Subject":      []string{"Quality check"},
				"Content-Type": []string{"multipart/alternative"},
			},
		},
		MIME: &data.MIMEBody{
			Parts: []*data.Content{
				{
					Headers: map[string][]string{"Content-Type": []string{"text/plain"}},
					Body:    "Plain fallback",
				},
				{
					Headers: map[string][]string{"Content-Type": []string{"text/html"}},
					Body:    "<p>HTML body</p>",
				},
			},
		},
	}

	input := emailQualityInputFromMessage(message)
	if input.Subject != "Quality check" {
		t.Fatalf("expected subject Quality check, got %q", input.Subject)
	}
	if input.PlainText != "Plain fallback" {
		t.Fatalf("expected plain text fallback, got %q", input.PlainText)
	}
	if input.HTML != "<p>HTML body</p>" {
		t.Fatalf("expected HTML body, got %q", input.HTML)
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
	return newTestMessageWithTag(id, folder, "")
}

func newTestMessageWithTag(id, folder, tag string) data.Message {
	if tag == "" {
		return newTestMessageWithTags(id, folder, []string{})
	}
	return newTestMessageWithTags(id, folder, []string{tag})
}

func newTestMessageWithTags(id, folder string, tags []string) data.Message {
	headers := map[string][]string{}
	if folder != "" {
		headers[folderHeaderName] = []string{folder}
	}
	if len(tags) > 0 {
		headers[tagHeaderName] = tags
	}
	return data.Message{
		ID: data.MessageID(id),
		Content: &data.Content{
			Headers: headers,
		},
	}
}

func newTestMessageWithBody(id, folder, body string) data.Message {
	msg := newTestMessageWithTags(id, folder, []string{})
	if msg.Content == nil {
		msg.Content = &data.Content{Headers: map[string][]string{}}
	}
	msg.Content.Body = body
	return msg
}
