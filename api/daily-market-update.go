package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
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

type telegramMessage struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

type yahooQuoteResponse struct {
	QuoteResponse struct {
		Result []struct {
			Symbol                 string  `json:"symbol"`
			RegularMarketPrice     float64 `json:"regularMarketPrice"`
			RegularMarketChange    float64 `json:"regularMarketChange"`
			RegularMarketChangePct float64 `json:"regularMarketChangePercent"`
		} `json:"result"`
	} `json:"quoteResponse"`
}

// Handler is the entrypoint for this Vercel function.
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

	allSymbols := make([]string, 0, len(indices)+len(marketWatch))
	for _, idx := range indices {
		allSymbols = append(allSymbols, idx.symbol)
	}
	for _, asset := range marketWatch {
		allSymbols = append(allSymbols, asset.symbol)
	}

	quoteMap, err := fetchYahooQuotes(allSymbols)
	if err != nil {
		log.Println("failed to fetch Yahoo quotes:", err)
		http.Error(w, "failed to fetch market data", http.StatusInternalServerError)
		return
	}

	indicesLines := make([]string, 0, len(indices))
	for _, idx := range indices {
		snap, ok := quoteMap[idx.symbol]
		if !ok {
			log.Printf("no quote data for %s (%s)", idx.name, idx.symbol)
			indicesLines = append(indicesLines,
				fmt.Sprintf("- %s: (data unavailable)", html.EscapeString(idx.name)))
			continue
		}
		snap.Name = idx.name
		indicesLines = append(indicesLines, formatIndexLine(snap))
	}

	marketWatchLines := make([]string, 0, len(marketWatch))
	for _, asset := range marketWatch {
		snap, ok := quoteMap[asset.symbol]
		if !ok {
			log.Printf("no quote data for %s (%s)", asset.name, asset.symbol)
			marketWatchLines = append(marketWatchLines,
				fmt.Sprintf("- %s: (data unavailable)", html.EscapeString(asset.name)))
			continue
		}
		snap.Name = asset.name
		marketWatchLines = append(marketWatchLines, formatIndexLine(snap))
	}

	indicesSection := "<b>Major Indices</b>\n" + strings.Join(indicesLines, "\n")
	marketWatchSection := "<b>Market Watch</b>\n" + strings.Join(marketWatchLines, "\n")

	// Fetch tech news
	techDoc, err := fetchDocument("https://finance.yahoo.com/topic/tech/")
	techSection := "<b>Top Technology Articles</b>\n- (data unavailable)"
	if err != nil {
		log.Println("failed to fetch Yahoo tech topic page:", err)
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

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("telegram api returned status %s", resp.Status)
		}
		return fmt.Errorf("telegram api returned status %s: %s",
			resp.Status, bytes.TrimSpace(responseBody))
	}

	return nil
}

func fetchDocument(pageURL string) (*goquery.Document, error) {
	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	// Clean headers to bypass layout protection
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

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

func fetchYahooQuotes(symbols []string) (map[string]*IndexSnapshot, error) {
	if len(symbols) == 0 {
		return map[string]*IndexSnapshot{}, nil
	}

	encodedSymbols := make([]string, 0, len(symbols))
	for _, s := range symbols {
		encodedSymbols = append(encodedSymbols, url.QueryEscape(s))
	}
	query := strings.Join(encodedSymbols, ",")

	apiURL := "https://query1.finance.yahoo.com/v7/finance/quote?symbols=" + query

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build quote request: %w", err)
	}

	// Full fake user-agent replaces "Mozilla/5.0" to satisfy structural cookie/crumb checking
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform quote request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected quote status %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed yahooQuoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode quote json: %w", err)
	}

	result := make(map[string]*IndexSnapshot, len(parsed.QuoteResponse.Result))
	for _, q := range parsed.QuoteResponse.Result {
		price := fmt.Sprintf("%.2f", q.RegularMarketPrice)
		change := formatSignedFloat(q.RegularMarketChange)
		changePct := formatSignedPercent(q.RegularMarketChangePct)
		result[q.Symbol] = &IndexSnapshot{
			Price:     price,
			Change:    change,
			ChangePct: changePct,
		}
	}

	return result, nil
}

func formatSignedFloat(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.2f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func formatSignedPercent(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.2f%%", v)
	}
	return fmt.Sprintf("%.2f%%", v)
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

	// Target lists inside container template streams and static layout content cards
	doc.Find("section #stream-container-scroll-template li a, div.js-stream-content a").EachWithBreak(func(i int, anchor *goquery.Selection) bool {
		href, ok := anchor.Attr("href")
		if !ok {
			return true
		}

		link := normalizeYahooURL(href)
		if !isYahooArticleURL(link) {
			return true
		}

		if _, exists := seenURLs[link]; exists {
			return true
		}

		title := strings.TrimSpace(anchor.Text())
		title = strings.Join(strings.Fields(title), " ")
		
		// Drop brief structural labels ("News", "Tech") from the target pool
		if len(title) < 15 {
			return true
		}

		seenURLs[link] = struct{}{}
		articles = append(articles, ArticleHeadline{
			Title: title,
			URL:   link,
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

func isYahooArticleURL(link string) bool {
	if !strings.HasPrefix(link, "https://finance.yahoo.com/") {
		return false
	}
	// Focus cleanly on native news items and third-party media syndication routes
	return strings.Contains(link, "/news/") || strings.Contains(link, "/m/")
}

func formatArticleLines(articles []ArticleHeadline) string {
	lines := make([]string, 0, len(articles))
	for i, article := range articles {
		// Embeds the link tag directly inside the list numbering element layout wrapper
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