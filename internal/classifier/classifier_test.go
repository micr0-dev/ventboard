package classifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeLabels(t *testing.T) {
	t.Parallel()

	labels := normalizeLabels([]string{"grief", "none", "violence", "spam", "ascii-art", "Grief", "made-up"})
	if len(labels) != 3 {
		t.Fatalf("expected 3 labels, got %d", len(labels))
	}
	if labels[0] != "spam" || labels[1] != "violence" || labels[2] != "grief" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
}

func TestParseLabelsRejectsBadJSON(t *testing.T) {
	t.Parallel()

	if _, err := parseLabels("not-json"); err == nil {
		t.Fatal("expected parseLabels to fail")
	}
}

func TestParseLabelsTreatsNoneAsEmpty(t *testing.T) {
	t.Parallel()

	labels, err := parseLabels(`{"labels":["none"]}`)
	if err != nil {
		t.Fatalf("parseLabels returned error: %v", err)
	}
	if len(labels) != 0 {
		t.Fatalf("expected no labels, got %#v", labels)
	}
}

func TestMergeHeuristicLabelsAddsSpamAlongsideExistingLabels(t *testing.T) {
	t.Parallel()

	labels := mergeHeuristicLabels("BUY NOW http://example.com cheap knives cheap knives", []string{"violence"})
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %#v", labels)
	}
	if labels[0] != "spam" || labels[1] != "violence" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
}

func TestLooksLikeSpamFalseForNormalPost(t *testing.T) {
	t.Parallel()

	if looksLikeSpam("i had a hard night and i don't know what to do") {
		t.Fatal("normal post should not look like spam")
	}
}

func TestLooksLikeSpamFalseForRepeatedEmotionalLanguage(t *testing.T) {
	t.Parallel()

	body := "why is it that despite being medicated despite studying despite having friends despite having a safe home having food having everything i need i still feel myself filling with dread"
	if looksLikeSpam(body) {
		t.Fatal("repeated emotional language should not look like spam")
	}
}

func TestLooksLikeSpamFalseForSentencePeriodsWithoutSpaces(t *testing.T) {
	t.Parallel()

	body := "i've been feeling something. recently. i don't know why. but it's been creeping on me.when i lay in bed late. feeling like the world doesn't hold impact."
	if looksLikeSpam(body) {
		t.Fatal("sentence punctuation should not be mistaken for a URL")
	}
}

func TestMergeHeuristicLabelsAddsASCIIArt(t *testing.T) {
	t.Parallel()

	labels := mergeHeuristicLabels("||| k |||\n/// i ///\n\\\\ l \\\\\n*** l ***", nil)
	if len(labels) != 1 || labels[0] != "spam" {
		t.Fatalf("expected spam label from ascii-art heuristic, got %#v", labels)
	}
}

func TestNormalizeForClassificationJoinsSeparatedLetters(t *testing.T) {
	t.Parallel()

	normalized := normalizeForClassification("s e l f  h a r m")
	if normalized != "selfharm" {
		t.Fatalf("unexpected normalized output: %q", normalized)
	}
}

func TestNeedsNormalizedPassForASCIIArt(t *testing.T) {
	t.Parallel()

	body := "||| k |||\n/// i ///\n\\\\ l \\\\\n*** l ***"
	if !needsNormalizedPass(body) {
		t.Fatal("expected ascii-art style content to trigger normalized pass")
	}
}

func TestClientCategorizeMergesRawAndNormalizedLabels(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			w.Write([]byte(`{"response":"{\"labels\":[\"violence\"]}"}`))
			return
		}
		w.Write([]byte(`{"response":"{\"labels\":[\"spam\"]}"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "stub-model", time.Second)
	labels, err := client.Categorize(context.Background(), "b u y n o w !!! knives")
	if err != nil {
		t.Fatalf("Categorize returned error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 classify calls, got %d", calls.Load())
	}
	if len(labels) != 2 || labels[0] != "spam" || labels[1] != "violence" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
}

func TestClientCategorizeFallsBackToThinkingField(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":"","thinking":"{\"labels\":[\"grief\"]}"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "stub-model", time.Second)
	labels, err := client.Categorize(context.Background(), "i feel awful")
	if err != nil {
		t.Fatalf("Categorize returned error: %v", err)
	}
	if len(labels) != 1 || labels[0] != "grief" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
}
