package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type IndexSnapshot struct {
	Name      string
	Price     string
	Change    string
	ChangePct string
}

type ArticleHeadline struct {
	Title string
	URL   string
}

// telegramMessage represents the JSON body sent to Telegram.
type telegramMessage struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// Handler is the entrypoint for this Vercel function.
// Vercel maps /api/daily-market-update to this function.[web:87][web:37]
func Handler(w http.ResponseWriter, r *http.Request) {
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

	homeDoc, err := fetchDocument("https://finance.yahoo.com/")
	if err != nil {
		log.Println("failed to fetch Yahoo homepage:", err)
		http.Error(w, "failed to fetch market data", http.StatusInternalServerError)
		return
	}

	indices := []struct {
		name   string
		symbol string
	}{
		{name: "S&P 500", symbol: "^GSPC"},
		{name: "Dow Jones", symbol: "^DJI"},
		{name: "Nasdaq", symbol: "^IXIC"},
		{name: "Russell 2000", symbol: "^RUT"},
	}

	marketWatch := []struct {
		name   string
		symbol string
	}{
		{name: "VIX", symbol: "^VIX"},
		{name: "Gold", symbol: "GC=F"},
		{name: "Bitcoin", symbol: "BTC-USD"},
		{name: "Brent Crude", symbol: "BZ=F"},
	}

	indicesLines := make([]string, 0, len(indices))
	for _, index := range indices {
		snapshot, err := getMarketSnapshot(homeDoc, index.name, index.symbol)
		if err != nil {
			log.Printf("failed to get %s snapshot: %v", index.name, err)
			indicesLines = append(indicesLines, fmt.Sprintf("- %s: (data unavailable)", html.EscapeString(index.name)))
			continue
		}
		indicesLines = append(indicesLines, formatIndexLine(snapshot))
	}

	marketWatchLines := make([]string, 0, len(marketWatch))
	for _, asset := range marketWatch {
		snapshot, err := getMarketSnapshot(homeDoc, asset.name, asset.symbol)
		if err != nil {
			log.Printf("failed to get %s snapshot: %v", asset.name, err)
			marketWatchLines = append(marketWatchLines, fmt.Sprintf("- %s: (data unavailable)", html.EscapeString(asset.name)))
			continue
		}
		marketWatchLines = append(marketWatchLines, formatIndexLine(snapshot))
	}

	indicesSection := "<b>Major Indices</b>\n" + strings.Join(indicesLines, "\n")
	marketWatchSection := "<b>Market Watch</b>\n" + strings.Join(marketWatchLines, "\n")

	techDoc, err := fetchDocument("https://finance.yahoo.com/sectors/technology/articles/")
	techSection := "<b>Top Technology Articles</b>\n- (data unavailable)"
	if err != nil {
		log.Println("failed to fetch Yahoo technology articles page:", err)
	} else {
		articles := getTopArticleHeadlines(techDoc, 10)
		log.Printf("found %d tech articles", len(articles))
		if len(articles) > 0 {
			techSection = "<b>Top Technology Articles</b>\n" + formatArticleLines(articles)
		}
	}

	messageText := "US Market Daily Wrap\n\n" +
		indicesSection + "\n\n" +
		marketWatchSection + "\n\n" +
		techSection

	if err := sendTelegramMessage(botToken, chatID, messageText); err != nil {
		log.Println("failed to send telegram message:", err)
		http.Error(w, "failed to send message", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// sendTelegramMessage posts a message to the Telegram Bot API using JSON.[web:35][web:38]
func sendTelegramMessage(botToken, chatID, text string) error {
	msg := telegramMessage{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "HTML",
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
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s from Yahoo", resp.Status)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	return doc, nil
}

func getMarketSnapshot(doc *goquery.Document, name, symbol string) (*IndexSnapshot, error) {
	price := firstFieldText(doc, symbol, "regularMarketPrice")
	change := firstFieldText(doc, symbol, "regularMarketChange")
	changePct := firstFieldText(doc, symbol, "regularMarketChangePercent")

	if price == "" {
		return nil, fmt.Errorf("could not find regularMarketPrice field for %s (%s)", name, symbol)
	}

	return &IndexSnapshot{
		Name:      name,
		Price:     price,
		Change:    change,
		ChangePct: changePct,
	}, nil
}

func firstFieldText(doc *goquery.Document, symbol, field string) string {
	selector := fmt.Sprintf("[data-symbol='%s'][data-field='%s']", symbol, field)
	text := strings.TrimSpace(doc.Find(selector).First().Text())
	if text != "" {
		return text
	}

	selector = fmt.Sprintf("[data-symbol=\"%s\"][data-field=\"%s\"]", symbol, field)
	return strings.TrimSpace(doc.Find(selector).First().Text())
}

func formatIndexLine(idx *IndexSnapshot) string {
	return fmt.Sprintf(
		"- %s: %s (%s, %s)",
		html.EscapeString(idx.Name),
		html.EscapeString(idx.Price),
		html.EscapeString(idx.Change),
		html.EscapeString(idx.ChangePct),
	)
}

func getTopArticleHeadlines(doc *goquery.Document, limit int) []ArticleHeadline {
	articles := make([]ArticleHeadline, 0, limit)
	seenURLs := make(map[string]struct{})

	doc.Find("a[href]").EachWithBreak(func(_ int, anchor *goquery.Selection) bool {
		href, ok := anchor.Attr("href")
		if !ok {
			return true
		}

		url := normalizeYahooURL(href)
		if !isYahooArticleURL(url) {
			return true
		}

		if _, exists := seenURLs[url]; exists {
			return true
		}

		title := strings.TrimSpace(anchor.Text())
		title = strings.Join(strings.Fields(title), " ")
		if title == "" {
			return true
		}

		seenURLs[url] = struct{}{}
		articles = append(articles, ArticleHeadline{
			Title: title,
			URL:   url,
		})

		return len(articles) < limit
	})

	return articles
}

func normalizeYahooURL(href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}

	if strings.HasPrefix(href, "/") {
		return "https://finance.yahoo.com" + href
	}

	return href
}

func isYahooArticleURL(url string) bool {
	return strings.HasPrefix(url, "https://finance.yahoo.com/sectors/technology/articles/")
}

func formatArticleLines(articles []ArticleHeadline) string {
	lines := make([]string, 0, len(articles))
	for i, article := range articles {
		lines = append(lines,
			fmt.Sprintf("%d. <a href=\"%s\">%s</a>",
				i+1,
				html.EscapeString(article.URL),
				html.EscapeString(article.Title),
			),
		)
	}
	return strings.Join(lines, "\n")
}