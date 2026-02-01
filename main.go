package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var (
	Token        = os.Getenv("DISCORD_TOKEN")
	LogChannelID = os.Getenv("LOG_CHANNEL_ID")
	MongoURI     = os.Getenv("MONGO_URI")
	MsgCol       *mongo.Collection
)

type ModmailLog struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	UserID    string        `bson:"user_id"`
	Username  string        `bson:"username"`
	Content   string        `bson:"content"`
	Timestamp time.Time     `bson:"timestamp"`
	Type      string        `bson:"type"`
}

func main() {
	if Token == "" || LogChannelID == "" || MongoURI == "" {
		log.Fatal("Missing environment variables: DISCORD_TOKEN, LOG_CHANNEL_ID, or MONGO_URI")
	}

	// --- FIXED CONTEXT USAGE ---
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pass ctx directly into Connect
	client, err := mongo.Connect(options.Client().ApplyURI(MongoURI))
	if err != nil {
		log.Fatal("MongoDB Connection Error:", err)
	}

	// Ping the database to ensure connection is actually active
	if err := client.Ping(ctx, nil); err != nil {
		log.Fatal("Could not ping MongoDB:", err)
	}

	MsgCol = client.Database("modmail_db").Collection("messages")
	fmt.Println("Successfully connected to MongoDB!")

	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		log.Fatal("Discord session error:", err)
	}

	dg.AddHandler(messageCreate)
	dg.Identify.Intents = discordgo.IntentsDirectMessages | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	if err = dg.Open(); err != nil {
		log.Fatal("Discord connection error:", err)
	}

	go func() {
		port := os.Getenv("PORT")
		if port == "" {
			port = "10000"
		}
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "Bot & DB Online")
		})
		log.Printf("Health check listening on port %s", port)
		http.ListenAndServe(":"+port, nil)
	}()

	fmt.Println("Bot is running. Press CTRL-C to exit.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-stop

	dg.Close()
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	if m.GuildID == "" {
		logMessage(m.Author.ID, m.Author.Username, m.Content, "incoming")
		content := fmt.Sprintf("📩 **New Modmail**\n**From:** %s (`%s`)\n\n%s",
			m.Author.Username, m.Author.ID, m.Content)
		s.ChannelMessageSend(LogChannelID, content)
		s.ChannelMessageSend(m.ChannelID, "✅ Sent to staff!")
		return
	}

	if m.ChannelID == LogChannelID && strings.HasPrefix(m.Content, "!reply ") {
		parts := strings.SplitN(m.Content, " ", 3)
		if len(parts) < 3 {
			s.ChannelMessageSend(m.ChannelID, "⚠️ Use: `!reply [UserID] [Message]`")
			return
		}

		userID, replyText := parts[1], parts[2]
		userChannel, err := s.UserChannelCreate(userID)
		if err != nil {
			s.ChannelMessageSend(m.ChannelID, "❌ User not found.")
			return
		}

		if _, err = s.ChannelMessageSend(userChannel.ID, "💬 **Staff:** "+replyText); err == nil {
			logMessage(userID, "STAFF", replyText, "reply")
			s.ChannelMessageSend(m.ChannelID, "✅ Reply sent.")
		}
	}
}

func logMessage(uid, user, content, msgType string) {
	entry := ModmailLog{
		UserID:    uid,
		Username:  user,
		Content:   content,
		Timestamp: time.Now(),
		Type:      msgType,
	}
	// Using context.Background() here since this is a background fire-and-forget task
	_, err := MsgCol.InsertOne(context.Background(), entry)
	if err != nil {
		log.Println("DB Log Error:", err)
	}
}
