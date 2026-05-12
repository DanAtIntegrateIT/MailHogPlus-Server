package api

import (
	"testing"

	"github.com/mailhog/data"
)

func TestGetMIMEPartByPathTopLevelAndNested(t *testing.T) {
	message := &data.Message{
		MIME: &data.MIMEBody{
			Parts: []*data.Content{
				{
					Headers: map[string][]string{"Content-Type": {"text/plain"}},
					Body:    "plain",
				},
				{
					Headers: map[string][]string{"Content-Type": {"multipart/mixed"}},
					MIME: &data.MIMEBody{
						Parts: []*data.Content{
							{
								Headers: map[string][]string{"Content-Type": {"text/html"}},
								Body:    "<html></html>",
							},
							{
								Headers: map[string][]string{"Content-Type": {"application/pdf"}},
								Body:    "pdfdata",
							},
						},
					},
				},
			},
		},
	}

	top, ok := getMIMEPartByPath(message, "0")
	if !ok || top == nil {
		t.Fatalf("expected top-level part 0")
	}
	if top.Body != "plain" {
		t.Fatalf("unexpected top-level body %q", top.Body)
	}

	nested, ok := getMIMEPartByPath(message, "1.1")
	if !ok || nested == nil {
		t.Fatalf("expected nested part 1.1")
	}
	if nested.Body != "pdfdata" {
		t.Fatalf("unexpected nested body %q", nested.Body)
	}
}

func TestGetMIMEPartByPathInvalid(t *testing.T) {
	message := &data.Message{
		MIME: &data.MIMEBody{
			Parts: []*data.Content{
				{Body: "plain"},
			},
		},
	}

	if part, ok := getMIMEPartByPath(message, "2"); ok || part != nil {
		t.Fatalf("expected invalid top-level path to fail")
	}
	if part, ok := getMIMEPartByPath(message, "0.1"); ok || part != nil {
		t.Fatalf("expected invalid nested path to fail")
	}
	if part, ok := getMIMEPartByPath(message, "abc"); ok || part != nil {
		t.Fatalf("expected non-numeric path to fail")
	}
}
