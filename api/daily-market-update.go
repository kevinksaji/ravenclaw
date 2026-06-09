package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type IndexSnapshot struct {
	Name      string
	Price     string
	Change    string
	ChangePct string
}

// telegramMessage represents the JSON body sent to Telegram
type telegramMessage struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

// Handler is the entrypoint for this Vercel function
// Vercel will map /api/daily-market-update to this function
func Handler(w http.ResponseWriter, r *http.Request) {
	// only allow GET (Vercel cron will call with GET)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")

	if botToken == "" || chatID == "" {
		log.Println("missing TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID")
		http.Error(w, "server not configured", http.StatusInternalServerError)
		return
	}

	indices := []struct {
		name string
		url  string
	}{
		{name: "S&P 500", url: "https://finance.yahoo.com/quote/%5EGSPC/"},
		{name: "Dow Jones", url: "https://finance.yahoo.com/quote/%5EDJI/"},
		{name: "Nasdaq", url: "https://finance.yahoo.com/quote/%5EIXIC/"},
	}

	type indexResult struct {
		snapshot *IndexSnapshot
		err      error
	}

	results := make([]indexResult, len(indices))
	var wg sync.WaitGroup
	wg.Add(len(indices))

	for i, index := range indices {
		go func(i int, index struct {
			name string
			url  string
		}) {
			defer wg.Done()
			snapshot, err := getIndexSnapshot(index.name, index.url)
			results[i] = indexResult{snapshot: snapshot, err: err}
		}(i, index)
	}

	wg.Wait()

	indicesLines := make([]string, 0, len(indices))
	for i, index := range indices {
		result := results[i]
		if result.err != nil {
			log.Printf("failed to get %s snapshot: %v", index.name, result.err)
			indicesLines = append(indicesLines, fmt.Sprintf("- %s: (data unavailable)", index.name))
			continue
		}

		indicesLines = append(indicesLines, formatIndexLine(result.snapshot))
	}

	indicesSection := "Major Indices:\n" + strings.Join(indicesLines, "\n")

	messageText := "US Market Daily Wrap\n\n" + indicesSection

	if err := sendTelegramMessage(botToken, chatID, messageText); err != nil {
		log.Println("failed to send telegram message:", err)
		http.Error(w, "failed to send message", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte("ok"))
}

// sendTelegramMessage post a message to the Teelegram Bot API using JSON
func sendTelegramMessage(botToken, chatID, text string) error {
	msg := telegramMessage{
		ChatID: chatID,
		Text:   text,
	}

	body, err := json.Marshal(msg)

	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))

	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("telegram api returned status %s", resp.Status)
		}

		return fmt.Errorf("telegram api returned status %s: %s", resp.Status, bytes.TrimSpace(responseBody))
	}

	return nil
}

func fetchDocument(url string) (*goquery.Document, error) {
	// A bare http.Get often looks like a bot request to sites such as Yahoo.
	// Building the request explicitly lets us add headers that better match a
	// normal browser visit and makes the failure mode easier to inspect.
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	// These headers do not guarantee access, but they make the request closer to
	// what Yahoo expects from a real browser session.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	// A timeout keeps the cron from hanging forever if the upstream site becomes
	// slow or stops responding.
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Do(req)

	if err != nil {
		return nil, fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s from Yahoo", resp.Status)
	}

	// goquery can parse the HTML directly from the response body stream.
	doc, err := goquery.NewDocumentFromReader(resp.Body)

	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	return doc, nil
}

func getIndexSnapshot(name, url string) (*IndexSnapshot, error) {
	doc, err := fetchDocument(url)
	if err != nil {
		return nil, err
	}

	price := firstFieldText(doc, "regularMarketPrice")
	change := firstFieldText(doc, "regularMarketChange")
	changePct := firstFieldText(doc, "regularMarketChangePercent")

	if price == "" {
		return nil, fmt.Errorf("could not find regularMarketPrice field for %s", name)
	}

	return &IndexSnapshot{
		Name:      name,
		Price:     price,
		Change:    change,
		ChangePct: changePct,
	}, nil

}

func firstFieldText(doc *goquery.Document, field string) string {
	// Build a CSS selector like [data-field='regularMarketPrice'] and return the
	// text from the first matching Yahoo element.
	selector := fmt.Sprintf("[data-field='%s']", field)
	text := strings.TrimSpace(doc.Find(selector).First().Text())
	if text != "" {
		return text
	}

	// Try the same selector with double quotes as a small fallback in case the
	// page markup is emitted in a slightly different form.
	selector = fmt.Sprintf("[data-field=\"%s\"]", field)
	return strings.TrimSpace(doc.Find(selector).First().Text())
}

func formatIndexLine(idx *IndexSnapshot) string {
	// e.g. "S&P 500: 5,000.12 (+12.34, +0.20%)"
	return fmt.Sprintf("- %s: %s (%s, %s)", idx.Name, idx.Price, idx.Change, idx.ChangePct)
}
