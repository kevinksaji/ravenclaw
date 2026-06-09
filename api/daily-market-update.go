package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/PuerkitoBio/goquery"
)

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

	indicesInfo := "failed to load Yahoo page"

	doc, err := fetchDocument("https://finance.yahoo.com/quote/%5EGSPC/")

	if err != nil {
		log.Println("failed to fetch Yahoo page:", err)
	} else {
		title := doc.Find("title").Text()
		if title != "" {
			indicesInfo = fmt.Sprintf("Yahoo page title: %s", title)
		}
	}

	messageText := buildStaticMessage() + "\n\n" + indicesInfo

	if err := sendTelegramMessage(botToken, chatID, messageText); err != nil {
		log.Println("failed to send telegram message:", err)
		http.Error(w, "failed to send message", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte("ok"))
}

// buildStaticMessage returns a placeholder message for now, will be replaced with real templated summary using scraped data
func buildStaticMessage() string {
	return `US Market Daily Wrap - (demo)
	Major Indices:
- S&P 500: 5,000 (+0.5%)
- Dow Jones: 38,000 (+0.2%)
- Nasdaq: 16,000 (+0.8%)

Top Movers:
- Biggest Gainer: DEMO +10.0%
- Biggest Loser: DEMO2 -8.5%

Headlines:
1. Demo headline 1
2. Demo headline 2
3. Demo headline 3
	`
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
