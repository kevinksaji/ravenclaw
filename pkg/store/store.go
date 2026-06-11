package store

type WatchlistStore interface {
	AddTickers(chatID string, tickers []string) error
	GetTickers(chatID string) ([]string, error)
	RemoveTickers(chatID string, tickers []string) error
}
