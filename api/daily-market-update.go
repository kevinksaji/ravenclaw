package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"ravenclaw/pkg/bot"
	"ravenclaw/pkg/store"
	"ravenclaw/pkg/telegram"
)

// Handler is the entrypoint for this Vercel function.
func Handler(w http.ResponseWriter, r *http.Request) {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	defaultChatID := os.Getenv("TELEGRAM_CHAT_ID")

	if botToken == "" {
		log.Println("missing TELEGRAM_BOT_TOKEN")
		http.Error(w, "server not configured", http.StatusInternalServerError)
		return
	}

	var watchlistStore store.WatchlistStore
	// Vercel Postgres provides POSTGRES_URL. Prisma typically uses DATABASE_URL.
	dbURL := os.Getenv("POSTGRES_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}

	if dbURL == "" {
		log.Println("missing POSTGRES_URL or DATABASE_URL")
		http.Error(w, "database not configured", http.StatusInternalServerError)
		return
	}

	var err error
	watchlistStore, err = store.NewPostgresStore(dbURL)
	if err != nil {
		log.Printf("Failed to connect to database: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	tgBot := bot.NewBot(botToken, watchlistStore)

	// Handle Webhook updates from Telegram
	if r.Method == http.MethodPost {
		webhookSecret := os.Getenv("TELEGRAM_WEBHOOK_SECRET")
		if webhookSecret != "" {
			if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != webhookSecret {
				log.Println("unauthorized webhook access attempt")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		var update telegram.Update
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			log.Printf("Failed to decode update: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		tgBot.HandleUpdate(&update)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	// Handle CRON job trigger (GET request)
	if r.Method == http.MethodGet {
		if defaultChatID == "" {
			log.Println("missing TELEGRAM_CHAT_ID for cron job")
			http.Error(w, "server not configured", http.StatusInternalServerError)
			return
		}

		msg, err := tgBot.GenerateMarketUpdate(defaultChatID)
		if err != nil {
			log.Printf("failed to generate market update: %v", err)
			http.Error(w, "failed to generate market data", http.StatusInternalServerError)
			return
		}

		if err := telegram.SendMessage(botToken, defaultChatID, msg); err != nil {
			log.Printf("failed to send telegram message: %v", err)
			http.Error(w, "failed to send message", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
