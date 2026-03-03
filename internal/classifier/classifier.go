package classifier

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ventboard/internal/posts"
)

var labelOrder = []string{
	"spam",
	"violence",
	"self-harm",
	"suicidal-ideation",
	"sexual-content",
	"abuse",
	"eating-disorder",
	"substance-use",
	"graphic-medical",
	"grief",
}

type Categorizer interface {
	Categorize(ctx context.Context, body string) ([]string, error)
	Version() string
}

type Client struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

func NewClient(baseURL, model string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *Client) Version() string {
	return c.model
}

func (c *Client) Categorize(ctx context.Context, body string) ([]string, error) {
	rawLabels, err := c.classifyText(ctx, body)
	if err != nil {
		return nil, err
	}

	labels := rawLabels
	if needsNormalizedPass(body) {
		normalized := normalizeForClassification(body)
		if normalized != "" && normalized != body {
			if normalizedLabels, err := c.classifyText(ctx, normalized); err == nil {
				labels = mergeLabelSets(labels, normalizedLabels)
			}
		}
	}

	return mergeHeuristicLabels(body, labels), nil
}

func (c *Client) classifyText(ctx context.Context, body string) ([]string, error) {
	requestBody := map[string]any{
		"model":  c.model,
		"stream": false,
		"format": "json",
		"options": map[string]any{
			"temperature": 0,
		},
		"prompt": buildPrompt(body),
	}

	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call ollama: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read ollama response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	var generateResp struct {
		Response string `json:"response"`
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(responseBody, &generateResp); err != nil {
		return nil, fmt.Errorf("decode ollama envelope: %w", err)
	}

	modelOutput := strings.TrimSpace(generateResp.Response)
	if modelOutput == "" {
		modelOutput = strings.TrimSpace(generateResp.Thinking)
	}

	labels, err := parseLabels(modelOutput)
	if err != nil {
		return nil, err
	}
	return labels, nil
}

type Worker struct {
	repo         *posts.Repository
	categorizer  Categorizer
	pollInterval time.Duration
	maxRetries   int
	logger       *log.Logger
	now          func() time.Time
}

func NewWorker(repo *posts.Repository, categorizer Categorizer, pollInterval time.Duration, maxRetries int, logger *log.Logger) *Worker {
	return &Worker{
		repo:         repo,
		categorizer:  categorizer,
		pollInterval: pollInterval,
		maxRetries:   maxRetries,
		logger:       logger,
		now:          time.Now,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		if _, err := w.ProcessNext(ctx); err != nil && w.logger != nil {
			w.logger.Printf("classifier worker error: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) ProcessNext(ctx context.Context) (bool, error) {
	post, err := w.repo.NextPending(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("fetch pending post: %w", err)
	}

	labels, err := w.categorizer.Categorize(ctx, post.Body)
	if err != nil {
		if markErr := w.repo.MarkClassificationFailure(ctx, post.ID, err.Error(), w.maxRetries); markErr != nil {
			return true, fmt.Errorf("mark classification failure: %w", markErr)
		}
		return true, nil
	}

	if err := w.repo.MarkPublished(ctx, post.ID, labels, w.categorizer.Version(), w.now().UTC()); err != nil {
		return true, fmt.Errorf("mark post published: %w", err)
	}

	return true, nil
}

func buildPrompt(body string) string {
	return fmt.Sprintf(`You are a content warning classifier for an anonymous text board.
If you are a reasoning model, do your thinking internally first.
Return JSON only with this schema:
{"labels":["violence","grief"]}

Rules:
- Choose zero or more labels from this exact list:
  ascii-art, spam, violence, self-harm, suicidal-ideation, sexual-content, abuse, eating-disorder, substance-use, graphic-medical, grief, none
- Use "none" only when no other label applies.
- Multiple labels can apply to the same post.
- Use "ascii-art" for ASCII art, symbol-heavy text patterns, or heavily obfuscated text that depends on layout, spacing, or character substitution to be read.
- "spam" is not exclusive. If a post is spam and also violent, abusive, sexual, or otherwise sensitive, include both "spam" and the other applicable labels.
- Use "spam" for obvious ads, scams, phishing, repetitive promotion, gibberish flooding, or posts that are plainly trying to clutter the board rather than say anything.
- Your final answer must be JSON only.
- Do not put the JSON in markdown fences.
- Do not put any explanation before or after the JSON.
- Do not include explanations.
- Do not include labels not in the list.

Post:
%s`, body)
}

func parseLabels(raw string) ([]string, error) {
	var parsed struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return nil, fmt.Errorf("decode classifier json: %w", err)
	}

	return normalizeLabels(parsed.Labels), nil
}

func normalizeLabels(labels []string) []string {
	allowed := make(map[string]int, len(labelOrder))
	for i, label := range labelOrder {
		allowed[label] = i
	}

	found := map[string]struct{}{}
	for _, label := range labels {
		normalized := strings.ToLower(strings.TrimSpace(label))
		normalized = strings.ReplaceAll(normalized, "_", "-")
		normalized = strings.ReplaceAll(normalized, " ", "-")
		if normalized == "ascii-art" {
			normalized = "spam"
		}
		if normalized == "none" {
			if len(labels) == 1 {
				return nil
			}
			continue
		}
		if _, ok := allowed[normalized]; ok {
			found[normalized] = struct{}{}
		}
	}

	result := make([]string, 0, len(found))
	for label := range found {
		result = append(result, label)
	}

	sort.Slice(result, func(i, j int) bool {
		return allowed[result[i]] < allowed[result[j]]
	})

	return result
}

func mergeHeuristicLabels(body string, labels []string) []string {
	var heuristicLabels []string
	if looksLikeASCIIArt(body) || hasSeparatedLetterRun(body) {
		heuristicLabels = append(heuristicLabels, "spam")
	}
	if looksLikeSpam(body) {
		heuristicLabels = append(heuristicLabels, "spam")
	}
	if len(heuristicLabels) == 0 {
		return labels
	}
	return mergeLabelSets(labels, heuristicLabels)
}

func looksLikeSpam(body string) bool {
	normalized := strings.ToLower(strings.TrimSpace(body))
	if normalized == "" {
		return false
	}

	if containsURL(normalized) {
		return true
	}

	spamPhrases := []string{
		"buy now",
		"limited time",
		"click here",
		"dm me for",
		"earn money fast",
		"work from home",
		"promo code",
		"free crypto",
		"guaranteed returns",
		"subscribe now",
		"discount code",
		"call now",
		"telegram.me/",
		"whatsapp",
	}
	for _, phrase := range spamPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}

	lines := strings.Split(normalized, "\n")
	lineCounts := map[string]int{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) < 8 {
			continue
		}
		lineCounts[line]++
		if lineCounts[line] >= 2 {
			return true
		}
	}

	words := strings.Fields(normalized)
	if len(words) >= 18 && hasSpammyWordRepetition(words) {
		return true
	}

	return hasExcessiveDigits(normalized)
}

func containsURL(value string) bool {
	commonTLDs := map[string]struct{}{
		"com": {}, "net": {}, "org": {}, "io": {}, "co": {}, "gg": {}, "xyz": {},
		"app": {}, "dev": {}, "me": {}, "tv": {}, "cc": {}, "ru": {}, "info": {},
	}

	for _, token := range strings.Fields(value) {
		token = strings.Trim(token, "[]()<>.,!?\"'")
		if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
			return true
		}
		if strings.HasPrefix(token, "www.") {
			return true
		}
		if strings.Contains(token, ".") && !strings.Contains(token, "@") {
			if !looksLikeLooseDomain(token, commonTLDs) {
				continue
			}
			if parsed, err := url.Parse("https://" + token); err == nil && parsed.Host != "" && strings.Contains(parsed.Host, ".") {
				return true
			}
		}
	}
	return false
}

func looksLikeLooseDomain(token string, commonTLDs map[string]struct{}) bool {
	if strings.Count(token, ".") == 0 {
		return false
	}
	for _, r := range token {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '.', '-', '/', ':':
		default:
			return false
		}
	}

	if strings.Contains(token, "/") || strings.Contains(token, ":") {
		return true
	}

	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return false
	}
	tld := parts[len(parts)-1]
	if _, ok := commonTLDs[tld]; !ok {
		return false
	}
	for _, part := range parts[:len(parts)-1] {
		if len(part) < 2 {
			return false
		}
	}
	return true
}

func trimWord(word string) string {
	return strings.TrimFunc(word, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func hasExcessiveDigits(value string) bool {
	digits := 0
	for _, r := range value {
		if _, err := strconv.Atoi(string(r)); err == nil {
			digits++
		}
	}
	return digits >= 12
}

func hasSpammyWordRepetition(words []string) bool {
	stopWords := map[string]struct{}{
		"about": {}, "after": {}, "again": {}, "because": {}, "before": {}, "being": {},
		"everything": {}, "feeling": {}, "friend": {}, "friends": {}, "having": {},
		"maybe": {}, "nothing": {}, "should": {}, "still": {}, "there": {}, "thing": {},
		"think": {}, "through": {}, "today": {}, "where": {}, "while": {}, "would": {},
	}

	wordCounts := map[string]int{}
	totalCounted := 0
	for _, word := range words {
		word = trimWord(word)
		if len(word) < 6 {
			continue
		}
		if _, ok := stopWords[word]; ok {
			continue
		}
		totalCounted++
		wordCounts[word]++
	}

	if totalCounted < 6 {
		return false
	}

	for word, count := range wordCounts {
		if count >= 6 && count*3 >= totalCounted {
			_ = word
			return true
		}
	}

	return false
}

func mergeLabelSets(sets ...[]string) []string {
	merged := make([]string, 0)
	for _, set := range sets {
		merged = append(merged, set...)
	}
	return normalizeLabels(merged)
}

func needsNormalizedPass(body string) bool {
	return hasSeparatedLetterRun(body) || hasLeetspeak(body) || looksLikeASCIIArt(body)
}

func normalizeForClassification(body string) string {
	lines := strings.Split(body, "\n")
	normalizedLines := make([]string, 0, len(lines))
	compactLines := make([]string, 0, len(lines))

	for _, line := range lines {
		mapped := mapObfuscatedRunes(line)
		words := joinSingleCharRuns(strings.Fields(mapped))
		cleaned := strings.Join(words, " ")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned != "" {
			normalizedLines = append(normalizedLines, cleaned)
		}

		compact := compactAlphaNumeric(mapped)
		if len(compact) >= 4 && compact != strings.ReplaceAll(cleaned, " ", "") {
			compactLines = append(compactLines, compact)
		}
	}

	var parts []string
	if len(normalizedLines) > 0 {
		parts = append(parts, strings.Join(normalizedLines, "\n"))
	}
	if len(compactLines) > 0 {
		parts = append(parts, strings.Join(compactLines, "\n"))
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func mapObfuscatedRunes(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))

	for _, r := range value {
		switch unicode.ToLower(r) {
		case '0':
			builder.WriteRune('o')
		case '1', '!', '|':
			builder.WriteRune('i')
		case '3':
			builder.WriteRune('e')
		case '4', '@':
			builder.WriteRune('a')
		case '5', '$':
			builder.WriteRune('s')
		case '7', '+':
			builder.WriteRune('t')
		case '8':
			builder.WriteRune('b')
		default:
			switch {
			case unicode.IsLetter(r):
				builder.WriteRune(unicode.ToLower(r))
			case unicode.IsDigit(r):
				builder.WriteRune(r)
			case unicode.IsSpace(r):
				builder.WriteRune(' ')
			default:
				builder.WriteRune(' ')
			}
		}
	}

	return strings.Join(strings.Fields(builder.String()), " ")
}

func joinSingleCharRuns(words []string) []string {
	result := make([]string, 0, len(words))
	var run []string

	flush := func() {
		if len(run) >= 3 {
			result = append(result, strings.Join(run, ""))
		} else {
			result = append(result, run...)
		}
		run = nil
	}

	for _, word := range words {
		if len([]rune(word)) == 1 {
			run = append(run, word)
			continue
		}
		if len(run) > 0 {
			flush()
		}
		result = append(result, word)
	}

	if len(run) > 0 {
		flush()
	}

	return result
}

func compactAlphaNumeric(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func hasSeparatedLetterRun(body string) bool {
	words := strings.Fields(mapObfuscatedRunes(body))
	runLength := 0
	for _, word := range words {
		if len([]rune(word)) == 1 {
			runLength++
			if runLength >= 4 {
				return true
			}
			continue
		}
		runLength = 0
	}
	return false
}

func hasLeetspeak(body string) bool {
	return strings.ContainsAny(strings.ToLower(body), "013457@$!+")
}

func looksLikeASCIIArt(body string) bool {
	lines := strings.Split(body, "\n")
	if len(lines) < 3 {
		return false
	}

	symbolHeavy := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 4 {
			continue
		}

		symbols := 0
		for _, r := range trimmed {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r)) {
				symbols++
			}
		}

		if float64(symbols)/float64(len([]rune(trimmed))) >= 0.35 {
			symbolHeavy++
		}
	}

	return symbolHeavy >= 2
}
