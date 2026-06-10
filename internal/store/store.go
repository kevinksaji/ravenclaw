package store

import (
	"encoding/json"
	"os"
	"sync"
)

type WatchlistStore interface {
	AddTickers(chatID string, tickers []string) error
	GetTickers(chatID string) ([]string, error)
}

type fileStore struct {
	mu       sync.RWMutex
	filePath string
}

func NewFileStore(filePath string) WatchlistStore {
	return &fileStore{
		filePath: filePath,
	}
}

func (fs *fileStore) load() (map[string][]string, error) {
	data := make(map[string][]string)
	b, err := os.ReadFile(fs.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return data, nil
		}
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &data); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func (fs *fileStore) save(data map[string][]string) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(fs.filePath, b, 0644)
}

func (fs *fileStore) AddTickers(chatID string, newTickers []string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	data, err := fs.load()
	if err != nil {
		return err
	}

	existing := make(map[string]struct{})
	for _, t := range data[chatID] {
		existing[t] = struct{}{}
	}

	for _, t := range newTickers {
		if _, ok := existing[t]; !ok {
			data[chatID] = append(data[chatID], t)
			existing[t] = struct{}{}
		}
	}

	return fs.save(data)
}

func (fs *fileStore) GetTickers(chatID string) ([]string, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	data, err := fs.load()
	if err != nil {
		return nil, err
	}
	return data[chatID], nil
}
