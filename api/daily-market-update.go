package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

// telegramMessage represents the JSON body sent to Telegram
type telegramMessage struct {
	ChatID string `json:"chat_id"`
	Text string `json:"text"`
	ParseMode string `json:"parse_mode"`
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
	}

	//TODO: replace this with real scraped data from Yahoo Finance
	messageText := buildStaticMessage()

	if err := sendTelegramMessage(botToken, chatID, messageText); err!= nil {
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
		ChatID:    chatID,
		Text:      text,
		ParseMode: "Markdown",
	}

	body, err := json.Marshal(msg)

	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	resp, err := http.Post(url, "applciation/json", bytes.NewBuffer(body))

	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram api returned status %s", resp.Status)
	}
	
	return nil
}