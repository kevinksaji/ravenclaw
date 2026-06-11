package bot

import (
	"fmt"
	"html"
	"log"
	"strings"

	"ravenclaw/pkg/market"
	"ravenclaw/pkg/store"
	"ravenclaw/pkg/telegram"
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

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	cmd := strings.TrimPrefix(strings.ToLower(fields[0]), "/")

	switch cmd {
	case "start":
		welcomeMsg := `Welcome to the Market Bot! 📈

I provide a daily wrap-up of the US market, including major indices, key market assets, prioritized news, and your custom watchlist.

<b>Available Commands:</b>
• m - Manually trigger a market update
• t [ticker...] - Add tickers to your watchlist (e.g., t AAPL MSFT)
• r [ticker...] - Remove tickers from your watchlist (e.g., r AAPL)`

		_ = telegram.SendMessage(b.Token, chatID, welcomeMsg)

	case "m":
		// Manual trigger
		msg, err := b.GenerateMarketUpdate(chatID)
		if err != nil {
			log.Printf("Error generating market update: %v", err)
			_ = telegram.SendMessage(b.Token, chatID, "Failed to generate market update.")
			return
		}
		_ = telegram.SendMessage(b.Token, chatID, msg)

	case "t":
		if len(fields) < 2 {
			return
		}
		var newTickers []string
		for _, t := range fields[1:] {
			t = strings.TrimSpace(t)
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

	case "r":
		if len(fields) < 2 {
			return
		}
		var removeTickers []string
		for _, t := range fields[1:] {
			t = strings.TrimSpace(t)
			if t != "" {
				removeTickers = append(removeTickers, strings.ToUpper(t))
			}
		}

		if len(removeTickers) > 0 {
			err := b.Store.RemoveTickers(chatID, removeTickers)
			if err != nil {
				log.Printf("Error removing tickers: %v", err)
				_ = telegram.SendMessage(b.Token, chatID, "Failed to remove tickers from watchlist.")
				return
			}
			msg := fmt.Sprintf("Removed from watchlist: %s", strings.Join(removeTickers, ", "))
			_ = telegram.SendMessage(b.Token, chatID, msg)
		}
	}
}

// GenerateMarketUpdate builds the full daily wrap message for a given chatID
func (b *Bot) GenerateMarketUpdate(chatID string) (string, error) {
	indices := []struct {
		name   string
		symbol string
	}{
		{name: "S&P 500", symbol: "^GSPC"},
		{name: "Dow", symbol: "^DJI"},
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

	formatQuoteLine := func(name string, snap *market.IndexSnapshot) string {
		emoji := "⚪️"
		if strings.HasPrefix(snap.Change, "+") {
			emoji = "🟢"
		} else if strings.HasPrefix(snap.Change, "-") {
			emoji = "🔴"
		}

		return fmt.Sprintf(
			"%s %s: %s (%s, %s)",
			emoji,
			html.EscapeString(name),
			html.EscapeString(snap.Price),
			html.EscapeString(snap.Change),
			html.EscapeString(snap.ChangePct),
		)
	}

	indicesLines := make([]string, 0, len(indices))
	for _, idx := range indices {
		snap, ok := quoteMap[idx.symbol]
		if !ok {
			indicesLines = append(indicesLines, fmt.Sprintf("⚪️ %s: (data unavailable)", html.EscapeString(idx.name)))
			continue
		}
		indicesLines = append(indicesLines, formatQuoteLine(idx.name, snap))
	}

	marketWatchLines := make([]string, 0, len(marketWatch))
	for _, asset := range marketWatch {
		snap, ok := quoteMap[asset.symbol]
		if !ok {
			marketWatchLines = append(marketWatchLines, fmt.Sprintf("⚪️ %s: (data unavailable)", html.EscapeString(asset.name)))
			continue
		}
		marketWatchLines = append(marketWatchLines, formatQuoteLine(asset.name, snap))
	}

	var watchlistLines []string
	if len(userWatchlist) > 0 {
		for _, ticker := range userWatchlist {
			snap, ok := quoteMap[ticker]
			if !ok {
				watchlistLines = append(watchlistLines, fmt.Sprintf("⚪️ %s: (data unavailable)", html.EscapeString(ticker)))
				continue
			}

			watchlistLines = append(watchlistLines, formatQuoteLine(ticker, snap))
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
