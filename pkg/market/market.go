package market

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
	"sort"
	"strings"
	"time"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

type IndexSnapshot struct {
	Name      string
	Price     string
	Change    string
	ChangePct string
}

type ArticleHeadline struct {
	Title   string
	URL     string
	PubDate time.Time
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

type yahooQuoteResponse struct {
	QuoteResponse struct {
		Result []struct {
			Symbol                 string  `json:"symbol"`
			ShortName              string  `json:"shortName"`
			LongName               string  `json:"longName"`
			RegularMarketPrice     float64 `json:"regularMarketPrice"`
			RegularMarketChange    float64 `json:"regularMarketChange"`
			RegularMarketChangePct float64 `json:"regularMarketChangePercent"`
		} `json:"result"`
	} `json:"quoteResponse"`
}

func FetchYahooQuotes(symbols []string) (map[string]*IndexSnapshot, error) {
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

		name := q.ShortName
		if name == "" {
			name = q.LongName
		}
		if name == "" {
			name = q.Symbol
		}

		result[q.Symbol] = &IndexSnapshot{
			Name:      name,
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

func FetchPrioritizedMarketNews() []ArticleHeadline {
	techFeed := "https://www.cnbc.com/id/19854910/device/rss/rss.html"
	secondaryFeeds := []string{
		"https://www.cnbc.com/id/100003114/device/rss/rss.html", // Finance/Business Wire
		"https://www.cnbc.com/id/15839135/device/rss/rss.html",  // Earnings Focus Wire
	}

	finalArticles := make([]ArticleHeadline, 0, 15)
	seenURLs := make(map[string]struct{})

	timeThreshold := time.Now().Add(-24 * time.Hour)
	const cnbcTimeLayout = "Mon, 02 Jan 2006 15:04:05 MST"

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

	sort.Slice(finalArticles, func(i, j int) bool {
		return finalArticles[i].PubDate.After(finalArticles[j].PubDate)
	})

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

	return ArticleHeadline{Title: cleanTitle, URL: cleanURL, PubDate: parsedTime}, true
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
