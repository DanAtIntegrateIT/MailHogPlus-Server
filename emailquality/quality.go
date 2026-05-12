package emailquality

import (
	"html"
	"math"
	"regexp"
	"strings"
)

type EmailQualityInput struct {
	HTML      string
	PlainText string
	Subject   string
	Headers   map[string][]string
}

type EmailQualityResult struct {
	Score          float64            `json:"score"`
	RagStatus      string             `json:"ragStatus"`
	Summary        string             `json:"summary"`
	Hints          []EmailQualityHint `json:"hints"`
	CriticalIssues []EmailQualityHint `json:"criticalIssues"`
}

type EmailQualityHint struct {
	Severity string `json:"severity"`
	Area     string `json:"area"`
	Message  string `json:"message"`
}

var (
	tagRE             = regexp.MustCompile(`(?is)<[^>]+>`)
	styleBlockRE      = regexp.MustCompile(`(?is)<style\b[^>]*>(.*?)</style>`)
	imgTagRE          = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	linkHrefRE        = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
	widthRE           = regexp.MustCompile(`(?is)(?:width\s*=\s*["']?([0-9]{3,4})|width\s*:\s*([0-9]{3,4})px)`)
	unsafeTagRE       = regexp.MustCompile(`(?is)<\s*(script|form|iframe|video|audio|object|embed|canvas|input|textarea|select|button)\b`)
	altAttrRE         = regexp.MustCompile(`(?is)\balt\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
	preheaderMarkerRE = regexp.MustCompile(`(?is)(preheader|mso-hide\s*:\s*all|display\s*:\s*none|opacity\s*:\s*0|font-size\s*:\s*1px)`)
)

func Score(input EmailQualityInput) EmailQualityResult {
	htmlBody := strings.TrimSpace(input.HTML)
	plainBody := strings.TrimSpace(input.PlainText)
	textBody := strings.TrimSpace(plainBody)
	if textBody == "" {
		textBody = textFromHTML(htmlBody)
	}

	scorer := &qualityScorer{
		score: 10,
		seen:  map[string]bool{},
	}

	hasHTML := htmlBody != ""
	if hasHTML {
		scoreUnsafeContent(scorer, htmlBody)
		scoreCompatibility(scorer, htmlBody)
		scoreImages(scorer, htmlBody, textBody)
		scoreLinks(scorer, htmlBody)
		scorePreheader(scorer, htmlBody)
		scoreMobile(scorer, htmlBody)
		if plainBody == "" {
			scorer.add("warning", "deliverability", "This HTML email does not include a plain-text fallback. Add a text/plain part for clients that cannot render HTML.")
		}
	}

	scoreMarketingCompliance(scorer, input.Subject, htmlBody, plainBody)
	scoreSpamSignals(scorer, input.Subject, htmlBody, plainBody)

	if !hasHTML && plainBody == "" {
		scorer.add("critical", "content", "This email appears to have no readable HTML or plain-text content.")
	}

	score := round1(clampFloat(scorer.score, 0, 10))
	status := ragStatus(score)
	if scorer.hints == nil {
		scorer.hints = []EmailQualityHint{}
	}
	if scorer.criticalIssues == nil {
		scorer.criticalIssues = []EmailQualityHint{}
	}
	result := EmailQualityResult{
		Score:          score,
		RagStatus:      status,
		Summary:        summaryFor(status, len(scorer.criticalIssues), len(scorer.hints)),
		Hints:          scorer.hints,
		CriticalIssues: scorer.criticalIssues,
	}
	return result
}

type qualityScorer struct {
	score          float64
	hints          []EmailQualityHint
	criticalIssues []EmailQualityHint
	seen           map[string]bool
}

func (s *qualityScorer) add(severity, area, message string) {
	key := severity + "|" + area + "|" + message
	if s.seen[key] {
		return
	}
	s.seen[key] = true

	hint := EmailQualityHint{
		Severity: severity,
		Area:     area,
		Message:  message,
	}

	switch severity {
	case "critical":
		s.score -= 2.2
		s.criticalIssues = append(s.criticalIssues, hint)
	case "warning":
		s.score -= 0.9
		s.hints = append(s.hints, hint)
	default:
		s.score -= 0.25
		hint.Severity = "info"
		s.hints = append(s.hints, hint)
	}
}

func scoreUnsafeContent(s *qualityScorer, htmlBody string) {
	if matches := unsafeTagRE.FindAllStringSubmatch(htmlBody, -1); len(matches) > 0 {
		seenTags := map[string]bool{}
		var tags []string
		for _, match := range matches {
			if len(match) > 1 {
				tag := strings.ToLower(match[1])
				if !seenTags[tag] {
					seenTags[tag] = true
					tags = append(tags, tag)
				}
			}
		}
		s.add("critical", "compatibility", "This email contains unsafe or unsupported content ("+strings.Join(tags, ", ")+"). Remove scripts, forms, embedded media, and interactive elements.")
	}
}

func scoreCompatibility(s *qualityScorer, htmlBody string) {
	lower := strings.ToLower(htmlBody)
	if strings.Contains(lower, "display:flex") || strings.Contains(lower, "display: flex") || strings.Contains(lower, "flex-direction") {
		s.add("warning", "compatibility", "This email uses flexbox, which may not render correctly in Outlook desktop. Use table-based layout for critical sections.")
	}
	if strings.Contains(lower, "display:grid") || strings.Contains(lower, "display: grid") || strings.Contains(lower, "grid-template") {
		s.add("warning", "compatibility", "This email uses CSS grid, which has poor support in several email clients. Use table-based layout for critical sections.")
	}
	if strings.Contains(lower, "position:fixed") || strings.Contains(lower, "position: fixed") {
		s.add("warning", "compatibility", "This email uses fixed positioning, which is unreliable in email clients. Use static table or block layouts instead.")
	}
	if strings.Contains(lower, "<svg") {
		s.add("warning", "compatibility", "This email includes SVG content. Many email clients block or fail to render SVG reliably; use PNG/JPEG fallbacks.")
	}
}

func scoreImages(s *qualityScorer, htmlBody, textBody string) {
	images := imgTagRE.FindAllString(htmlBody, -1)
	if len(images) == 0 {
		return
	}

	missingAlt := 0
	for _, img := range images {
		match := altAttrRE.FindStringSubmatch(img)
		alt := ""
		for i := 2; i < len(match); i++ {
			if match[i] != "" {
				alt = strings.TrimSpace(match[i])
				break
			}
		}
		if match == nil || alt == "" {
			missingAlt++
		}
	}
	if missingAlt > 0 {
		s.add("warning", "accessibility", pluralize(missingAlt, "image is", "images are")+" missing alt text. Add concise alt text for accessibility and image-blocked clients.")
	}

	textLen := len(strings.Fields(textBody))
	if len(images) > 0 && textLen < 12 {
		s.add("critical", "content", "This appears to be an image-only email. Add real text to improve accessibility, rendering, and deliverability.")
		return
	}
	if len(images) >= 5 && textLen < len(images)*18 {
		s.add("warning", "deliverability", "This appears to be an image-heavy email. Add more real text to improve deliverability and accessibility.")
	}
}

func scoreLinks(s *qualityScorer, htmlBody string) {
	matches := linkHrefRE.FindAllStringSubmatch(htmlBody, -1)
	if len(matches) == 0 {
		return
	}

	nonHTTPS := 0
	broken := 0
	unsafe := 0
	for _, match := range matches {
		href := firstNonEmpty(match[2], match[3], match[4])
		href = strings.TrimSpace(html.UnescapeString(href))
		lower := strings.ToLower(href)
		switch {
		case href == "" || href == "#":
			broken++
		case strings.HasPrefix(lower, "javascript:"):
			unsafe++
		case strings.HasPrefix(lower, "http://"):
			nonHTTPS++
		case strings.HasPrefix(lower, "mailto:"), strings.HasPrefix(lower, "tel:"), strings.HasPrefix(lower, "https:"), strings.HasPrefix(lower, "cid:"):
		default:
			broken++
		}
	}

	if unsafe > 0 {
		s.add("critical", "links", pluralize(unsafe, "unsafe JavaScript link was", "unsafe JavaScript links were")+" found. Remove JavaScript links from email content.")
	}
	if broken > 0 {
		s.add("warning", "links", pluralize(broken, "empty or broken-looking link was", "empty or broken-looking links were")+" found. Replace placeholder links with final destinations.")
	}
	if nonHTTPS > 0 {
		s.add("warning", "links", pluralize(nonHTTPS, "non-HTTPS link was", "non-HTTPS links were")+" found. Use HTTPS links only.")
	}
}

func scorePreheader(s *qualityScorer, htmlBody string) {
	if !preheaderMarkerRE.MatchString(htmlBody) {
		s.add("warning", "content", "The email does not appear to have a preheader. Add one to improve inbox presentation.")
		return
	}
	if len(strings.Fields(textFromHTML(htmlBody))) < 8 {
		s.add("info", "content", "The visible email text is very short. Check that the preheader and first sentence give useful inbox context.")
	}
}

func scoreMobile(s *qualityScorer, htmlBody string) {
	lower := strings.ToLower(htmlBody)
	hasMediaQuery := strings.Contains(lower, "@media")
	wideFixedWidth := false
	for _, match := range widthRE.FindAllStringSubmatch(htmlBody, -1) {
		for i := 1; i < len(match); i++ {
			if match[i] == "" {
				continue
			}
			if widthValue(match[i]) > 640 {
				wideFixedWidth = true
				break
			}
		}
	}
	if wideFixedWidth && !hasMediaQuery {
		s.add("warning", "mobile", "This email uses wide fixed-width layout without clear responsive rules. Add mobile-friendly width constraints or media queries.")
	}
}

func scoreMarketingCompliance(s *qualityScorer, subject, htmlBody, plainBody string) {
	combined := strings.ToLower(subject + " " + textFromHTML(htmlBody) + " " + plainBody)
	marketingTerms := []string{"newsletter", "sale", "discount", "offer", "promotion", "promo", "deal", "subscribe", "campaign", "limited time"}
	isMarketing := false
	for _, term := range marketingTerms {
		if strings.Contains(combined, term) {
			isMarketing = true
			break
		}
	}
	if isMarketing && !strings.Contains(combined, "unsubscribe") {
		s.add("critical", "deliverability", "This looks like a marketing email but no unsubscribe link was found. Add a clear unsubscribe link.")
	}
}

func scoreSpamSignals(s *qualityScorer, subject, htmlBody, plainBody string) {
	combined := strings.ToLower(subject + " " + textFromHTML(htmlBody) + " " + plainBody)
	terms := []string{"act now", "buy now", "free!!!", "guaranteed", "risk free", "winner", "congratulations", "cash bonus", "click here", "limited time only"}
	count := 0
	for _, term := range terms {
		if strings.Contains(combined, term) {
			count++
		}
	}
	if count >= 3 {
		s.add("warning", "content", "Several spammy phrases were found. Reduce promotional language and make the copy more specific.")
	} else if count > 0 {
		s.add("info", "content", "Some promotional wording was found. Check that the message sounds specific and not spam-like.")
	}

	if strings.Contains(subject, "!!!") || strings.Contains(subject, "???") || strings.Count(subject, "!") >= 4 {
		s.add("warning", "content", "The subject uses excessive punctuation. Tone it down to improve trust and deliverability.")
	}
}

func ragStatus(score float64) string {
	switch {
	case score >= 8:
		return "Green"
	case score >= 5:
		return "Amber"
	default:
		return "Red"
	}
}

func summaryFor(status string, criticalCount, hintCount int) string {
	if status == "Green" && criticalCount == 0 {
		return "No significant issues found. This email appears suitable for sending."
	}
	if criticalCount > 0 {
		return "Critical email quality issues were found. Fix these before sending."
	}
	if status == "Amber" {
		return "This email is usable, but a few issues may affect rendering, deliverability, or accessibility."
	}
	if hintCount > 0 {
		return "This email has several quality concerns that should be reviewed before sending."
	}
	return "No significant issues found. This email appears suitable for sending."
}

func textFromHTML(htmlBody string) string {
	if htmlBody == "" {
		return ""
	}
	withoutStyles := styleBlockRE.ReplaceAllString(htmlBody, " ")
	text := tagRE.ReplaceAllString(withoutStyles, " ")
	text = html.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

func widthValue(value string) int {
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func pluralize(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return strconvLike(count) + " " + plural
}

func strconvLike(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func clampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
