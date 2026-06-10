package bot

import (
	"fmt"
	"html"
	"log"
	"strings"

	"ravenclaw/internal/market"
	"ravenclaw/internal/store"
	"ravenclaw/internal/telegram"
)

type Bot struct {
	Token string
	Store store.WatchlistStore
}

func NewBot(token string, s store.WatchlistStore) *Bot {
	return &Bot{
		Token: token,
		Store: s,
	}
}

// HandleUpdate processes an incoming Telegram webhook update
func (b *Bot) HandleUpdate(update *telegram.Update) {
	if update.Message == nil {
		return
	}

	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return
	}

	chatID := fmt.Sprintf("%d", update.Message.Chat.ID)

	lines := strings.Split(text, "\n")
	cmd := strings.ToLower(strings.TrimSpace(lines[0]))

	if cmd == "m" && len(lines) == 1 {
		// Manual trigger
		msg, err := b.GenerateMarketUpdate(chatID)
		if err != nil {
			log.Printf("Error generating market update: %v", err)
			_ = telegram.SendMessage(b.Token, chatID, "Failed to generate market update.")
			return
		}
		_ = telegram.SendMessage(b.Token, chatID, msg)
		return
	}

	if cmd == "t" && len(lines) > 1 {
		// Add to watchlist
		var newTickers []string
		for _, line := range lines[1:] {
			t := strings.TrimSpace(line)
			if t != "" {
				newTickers = append(newTickers, strings.ToUpper(t))
			}
		}

		if len(newTickers) > 0 {
			err := b.Store.AddTickers(chatID, newTickers)
			if err != nil {
				log.Printf("Error adding tickers: %v", err)
				_ = telegram.SendMessage(b.Token, chatID, "Failed to add tickers to watchlist.")
				return
			}
			msg := fmt.Sprintf("Added to watchlist: %s", strings.Join(newTickers, ", "))
			_ = telegram.SendMessage(b.Token, chatID, msg)
		}
		return
	}
}

// GenerateMarketUpdate builds the full daily wrap message for a given chatID
func (b *Bot) GenerateMarketUpdate(chatID string) (string, error) {
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

	allSymbols := make([]string, 0)
	for _, idx := range indices {
		allSymbols = append(allSymbols, idx.symbol)
	}
	for _, asset := range marketWatch {
		allSymbols = append(allSymbols, asset.symbol)
	}

	userWatchlist, err := b.Store.GetTickers(chatID)
	if err != nil {
		log.Printf("Warning: failed to get watchlist for chat %s: %v", chatID, err)
	}

	for _, ticker := range userWatchlist {
		allSymbols = append(allSymbols, ticker)
	}

	quoteMap, err := market.FetchYahooQuotes(allSymbols)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Yahoo quotes: %w", err)
	}

	formatIndexLine := func(snap *market.IndexSnapshot) string {
		return fmt.Sprintf(
			"- %s: %s (%s, %s)",
			html.EscapeString(snap.Name),
			html.EscapeString(snap.Price),
			html.EscapeString(snap.Change),
			html.EscapeString(snap.ChangePct),
		)
	}

	indicesLines := make([]string, 0, len(indices))
	for _, idx := range indices {
		snap, ok := quoteMap[idx.symbol]
		if !ok {
			indicesLines = append(indicesLines, fmt.Sprintf("- %s: (data unavailable)", html.EscapeString(idx.name)))
			continue
		}
		snap.Name = idx.name
		indicesLines = append(indicesLines, formatIndexLine(snap))
	}

	marketWatchLines := make([]string, 0, len(marketWatch))
	for _, asset := range marketWatch {
		snap, ok := quoteMap[asset.symbol]
		if !ok {
			marketWatchLines = append(marketWatchLines, fmt.Sprintf("- %s: (data unavailable)", html.EscapeString(asset.name)))
			continue
		}
		snap.Name = asset.name
		marketWatchLines = append(marketWatchLines, formatIndexLine(snap))
	}

	var watchlistLines []string
	if len(userWatchlist) > 0 {
		for _, ticker := range userWatchlist {
			snap, ok := quoteMap[ticker]
			if !ok {
				watchlistLines = append(watchlistLines, fmt.Sprintf("- %s: (data unavailable)", html.EscapeString(ticker)))
				continue
			}
			// Let the name be from Yahoo Finance (ShortName/LongName/Symbol) and format the line to show name and ticker
			displayName := fmt.Sprintf("%s (%s)", snap.Name, snap.Symbol)
			watchlistLines = append(watchlistLines, fmt.Sprintf(
				"- %s: %s (%s, %s)",
				html.EscapeString(displayName),
				html.EscapeString(snap.Price),
				html.EscapeString(snap.Change),
				html.EscapeString(snap.ChangePct),
			))
		}
	}

	indicesSection := "<b>Major Indices</b>\n" + strings.Join(indicesLines, "\n")
	marketWatchSection := "<b>Market Watch</b>\n" + strings.Join(marketWatchLines, "\n")

	watchlistSection := ""
	if len(watchlistLines) > 0 {
		watchlistSection = "<b>Watchlist</b>\n" + strings.Join(watchlistLines, "\n") + "\n\n"
	}

	articles := market.FetchPrioritizedMarketNews()
	marketNewsSection := "<b>Market News</b>\n- (No articles published in the past 24 hours)"
	if len(articles) > 0 {
		lines := make([]string, 0, len(articles))
		for i, article := range articles {
			lines = append(lines, fmt.Sprintf("%d. <a href=\"%s\">%s</a>", i+1, html.EscapeString(article.URL), html.EscapeString(article.Title)))
		}
		marketNewsSection = "<b>Market News</b>\n" + strings.Join(lines, "\n")
	}

	messageText := "US Market Daily Wrap\n\n" +
		indicesSection + "\n\n" +
		marketWatchSection + "\n\n" +
		watchlistSection +
		marketNewsSection

	return messageText, nil
}
