package store

import (
	"database/sql"
	"strings"

	_ "github.com/lib/pq"
)

type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore connects to a Postgres database and creates the watchlists table if it doesn't exist.
func NewPostgresStore(dbURL string) (WatchlistStore, error) {
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, err
	}

	// Create a simple table to store chat watchlists as comma-separated values
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS watchlists (
			chat_id VARCHAR(50) PRIMARY KEY,
			tickers TEXT
		)
	`)
	if err != nil {
		return nil, err
	}

	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) AddTickers(chatID string, newTickers []string) error {
	// 1. Fetch existing tickers
	existing, err := s.GetTickers(chatID)
	if err != nil {
		return err
	}

	// 2. Merge and deduplicate
	tickerSet := make(map[string]struct{})
	for _, t := range existing {
		tickerSet[t] = struct{}{}
	}
	for _, t := range newTickers {
		tickerSet[t] = struct{}{}
	}

	var merged []string
	for t := range tickerSet {
		merged = append(merged, t)
	}

	tickersStr := strings.Join(merged, ",")

	// 3. Upsert into database
	_, err = s.db.Exec(`
		INSERT INTO watchlists (chat_id, tickers) 
		VALUES ($1, $2)
		ON CONFLICT (chat_id) 
		DO UPDATE SET tickers = $2
	`, chatID, tickersStr)

	return err
}

func (s *PostgresStore) GetTickers(chatID string) ([]string, error) {
	var tickersStr string
	err := s.db.QueryRow(`SELECT tickers FROM watchlists WHERE chat_id = $1`, chatID).Scan(&tickersStr)
	if err != nil {
		if err == sql.ErrNoRows {
			// No watchlist yet, return empty list
			return []string{}, nil
		}
		return nil, err
	}

	if tickersStr == "" {
		return []string{}, nil
	}

	return strings.Split(tickersStr, ","), nil
}

func (s *PostgresStore) RemoveTickers(chatID string, tickersToRemove []string) error {
	existing, err := s.GetTickers(chatID)
	if err != nil {
		return err
	}

	if len(existing) == 0 {
		return nil
	}

	toRemoveMap := make(map[string]struct{})
	for _, t := range tickersToRemove {
		toRemoveMap[t] = struct{}{}
	}

	var remaining []string
	for _, t := range existing {
		if _, shouldRemove := toRemoveMap[t]; !shouldRemove {
			remaining = append(remaining, t)
		}
	}

	if len(remaining) == 0 {
		// If the list is empty, we can just delete the row or update it to be empty
		_, err = s.db.Exec(`DELETE FROM watchlists WHERE chat_id = $1`, chatID)
		return err
	}

	tickersStr := strings.Join(remaining, ",")
	_, err = s.db.Exec(`
		UPDATE watchlists SET tickers = $2 WHERE chat_id = $1
	`, chatID, tickersStr)

	return err
}
