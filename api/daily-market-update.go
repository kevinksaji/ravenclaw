package api

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"
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

// RSS XML Architecture Specs
type RssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel Channel  `xml:"channel"`
}

type Channel struct {
	Items []RssItem `xml:"item"`
}

type RssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`
}

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

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

	// Pull combined rolling market news entries under strict 24-hour and channel rules
	articles := fetchPrioritizedMarketNews()
	marketNewsSection := "<b>Market News</b>\n- (No articles published in the past 24 hours)"
	if len(articles) > 0 {
		marketNewsSection = "<b>Market News</b>\n" + formatArticleLines(articles)
	}

	messageText := "US Market Daily Wrap\n\n" +
		indicesSection + "\n\n" +
		marketWatchSection + "\n\n" +
		marketNewsSection

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

func fetchYahooQuotes(symbols []string) (map[string]*IndexSnapshot, error) {
	if len(symbols) == 0 {
		return map[string]*IndexSnapshot{}, nil
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
	}

	initReq, err := http.NewRequest(http.MethodGet, "https://fc.yahoo.com", nil)
	if err != nil {
		return nil, fmt.Errorf("build init request: %w", err)
	}
	initReq.Header.Set("User-Agent", userAgent)
	
	initResp, err := client.Do(initReq)
	if err == nil {
		initResp.Body.Close()
	}

	crumbReq, err := http.NewRequest(http.MethodGet, "https://query2.finance.yahoo.com/v1/test/getcrumb", nil)
	if err != nil {
		return nil, fmt.Errorf("build crumb request: %w", err)
	}
	crumbReq.Header.Set("User-Agent", userAgent)

	crumbResp, err := client.Do(crumbReq)
	if err != nil {
		return nil, fmt.Errorf("perform crumb request: %w", err)
	}
	defer crumbResp.Body.Close()

	crumbBytes, err := io.ReadAll(crumbResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read crumb response: %w", err)
	}
	crumb := string(bytes.TrimSpace(crumbBytes))

	if crumb == "" || crumbResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to retrieve valid crumb, status: %s", crumbResp.Status)
	}

	encodedSymbols := make([]string, 0, len(symbols))
	for _, s := range symbols {
		encodedSymbols = append(encodedSymbols, url.QueryEscape(s))
	}
	query := strings.Join(encodedSymbols, ",")

	apiURL := fmt.Sprintf("https://query1.finance.yahoo.com/v7/finance/quote?symbols=%s&crumb=%s", query, url.QueryEscape(crumb))

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build quote request: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

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

func fetchSingleFeed(targetURL string) ([]RssItem, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status error: %s", resp.Status)
	}

	var feed RssFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, err
	}
	return feed.Channel.Items, nil
}

func fetchPrioritizedMarketNews() []ArticleHeadline {
	techFeed := "https://www.cnbc.com/id/19854910/device/rss/rss.html"
	secondaryFeeds := []string{
		"https://www.cnbc.com/id/100003114/device/rss/rss.html", // Finance/Business Wire
		"https://www.cnbc.com/id/15839135/device/rss/rss.html", // Earnings Focus Wire
	}

	finalArticles := make([]ArticleHeadline, 0, 15)
	seenURLs := make(map[string]struct{})

	// UPDATED: Shifted from rolling 12h to full rolling 24h trailing evaluation layout
	timeThreshold := time.Now().Add(-24 * time.Hour)
	const cnbcTimeLayout = "Mon, 02 Jan 2006 15:04:05 MST"

	// --- CHANNEL 1: Process and Prioritize Technology Sector Stream First ---
	if items, err := fetchSingleFeed(techFeed); err == nil {
		techCount := 0
		for _, item := range items {
			if techCount >= 5 { 
				break
			}
			headline, eligible := processItem(item, timeThreshold, cnbcTimeLayout, seenURLs)
			if eligible {
				seenURLs[headline.URL] = struct{}{}
				finalArticles = append(finalArticles, headline)
				techCount++
			}
		}
	} else {
		log.Printf("error resolving tech feed: %v", err)
	}

	// --- CHANNEL 2: Fill remaining gaps up to 15 from Finance and Earnings streams ---
	for _, feedURL := range secondaryFeeds {
		if len(finalArticles) >= 15 { 
			break
		}
		if items, err := fetchSingleFeed(feedURL); err == nil {
			feedCount := 0
			for _, item := range items {
				if feedCount >= 5 || len(finalArticles) >= 15 {
					break
				}
				headline, eligible := processItem(item, timeThreshold, cnbcTimeLayout, seenURLs)
				if eligible {
					seenURLs[headline.URL] = struct{}{}
					finalArticles = append(finalArticles, headline)
					feedCount++
				}
			}
		} else {
			log.Printf("error resolving channel feed %s: %v", feedURL, err)
		}
	}

	return finalArticles
}

func processItem(item RssItem, threshold time.Time, layout string, seen map[string]struct{}) (ArticleHeadline, bool) {
	cleanURL := strings.TrimSpace(item.Link)
	if _, duplicate := seen[cleanURL]; duplicate || cleanURL == "" {
		return ArticleHeadline{}, false
	}

	pubDateStr := strings.TrimSpace(item.PubDate)
	if strings.HasSuffix(pubDateStr, "GMT") {
		pubDateStr = strings.TrimSuffix(pubDateStr, "GMT") + "UTC"
	}

	parsedTime, err := time.Parse(layout, pubDateStr)
	if err != nil {
		parsedTime, err = time.Parse(time.RFC1123, pubDateStr)
	}

	if err != nil || parsedTime.Before(threshold) {
		return ArticleHeadline{}, false
	}

	cleanTitle := html.UnescapeString(item.Title)
	cleanTitle = strings.TrimSpace(strings.Join(strings.Fields(cleanTitle), " "))
	if cleanTitle == "" {
		return ArticleHeadline{}, false
	}

	return ArticleHeadline{Title: cleanTitle, URL: cleanURL}, true
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