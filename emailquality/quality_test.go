package emailquality

import "testing"

func TestScoreGreenEmail(t *testing.T) {
	result := Score(EmailQualityInput{
		Subject:   "Your order update",
		PlainText: "Your order has shipped. Track it using the secure link in this email.",
		HTML: `<html>
			<body>
				<div style="display:none;mso-hide:all;font-size:1px">Your order has shipped</div>
				<p>Your order has shipped. Track it using the secure link below.</p>
				<p><a href="https://example.com/track">Track order</a></p>
				<img src="https://example.com/logo.png" alt="Example">
			</body>
		</html>`,
	})

	if result.RagStatus != "Green" {
		t.Fatalf("expected Green, got %s with score %.1f", result.RagStatus, result.Score)
	}
	if result.Score < 8 {
		t.Fatalf("expected score >= 8, got %.1f", result.Score)
	}
	if len(result.CriticalIssues) != 0 {
		t.Fatalf("expected no critical issues, got %#v", result.CriticalIssues)
	}
}

func TestScoreRedEmail(t *testing.T) {
	result := Score(EmailQualityInput{
		Subject: "FREE!!! ACT NOW!!!",
		HTML: `<html>
			<body>
				<script>alert("x")</script>
				<form><input type="email"></form>
				<a href="javascript:alert(1)">Click here</a>
				<a href="http://example.com">Deal</a>
				<img src="hero.png">
			</body>
		</html>`,
	})

	if result.RagStatus != "Red" {
		t.Fatalf("expected Red, got %s with score %.1f", result.RagStatus, result.Score)
	}
	if len(result.CriticalIssues) == 0 {
		t.Fatalf("expected critical issues")
	}
}

func TestMarketingEmailNeedsUnsubscribe(t *testing.T) {
	result := Score(EmailQualityInput{
		Subject:   "Limited time offer",
		PlainText: "Limited time offer for subscribers.",
		HTML: `<html><body>
			<div style="display:none">Limited time offer</div>
			<p>Limited time offer for subscribers.</p>
			<a href="https://example.com">View offer</a>
		</body></html>`,
	})

	found := false
	for _, issue := range result.CriticalIssues {
		if issue.Area == "deliverability" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected missing unsubscribe critical issue, got %#v", result.CriticalIssues)
	}
}
