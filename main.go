package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var (
	Token      = os.Getenv("DISCORD_TOKEN")
	GuildID    = os.Getenv("STAFF_GUILD_ID")
	CategoryID = os.Getenv("CATEGORY_ID")
	MongoURI   = os.Getenv("MONGO_URI")
	MsgCol     *mongo.Collection
)

type ModmailLog struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	UserID    string        `bson:"user_id"`
	Content   string        `bson:"content"`
	HasFile   bool          `bson:"has_file"`
	Timestamp time.Time     `bson:"timestamp"`
	Sender    string        `bson:"sender"`
}

func main() {
	if Token == "" || GuildID == "" || CategoryID == "" || MongoURI == "" {
		log.Fatal("Missing required environment variables.")
	}

	client, err := mongo.Connect(options.Client().ApplyURI(MongoURI))
	if err != nil {
		log.Fatal(err)
	}
	MsgCol = client.Database("modmail_db").Collection("messages")

	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		log.Fatal(err)
	}

	dg.Identify.Intents = discordgo.IntentDirectMessages | discordgo.IntentGuildMessages | discordgo.IntentMessageContent | discordgo.IntentGuilds
	dg.AddHandler(messageCreate)

	err = dg.Open()
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		port := os.Getenv("PORT")
		if port == "" { port = "10000" }
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "Modmail Bot Active") })
		http.ListenAndServe(":"+port, nil)
	}()

	fmt.Println("Bot is live. Errors fixed.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-stop
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID { return }

	// 1. USER -> STAFF (Incoming DM)
	if m.GuildID == "" {
		reg, _ := regexp.Compile("[^a-zA-Z0-9]+")
		cleanName := strings.ToLower(reg.ReplaceAllString(m.Author.Username, ""))
		channelName := fmt.Sprintf("ticket-%s", cleanName)

		channels, _ := s.GuildChannels(GuildID)
		var targetChannel *discordgo.Channel
		for _, ch := range channels {
			if strings.Contains(ch.Topic, m.Author.ID) {
				targetChannel = ch
				break
			}
		}

		if targetChannel == nil {
			targetChannel, _ = s.GuildChannelCreateComplex(GuildID, discordgo.GuildChannelCreateData{
				Name: channelName, Type: discordgo.ChannelTypeGuildText, ParentID: CategoryID, Topic: "Modmail ID: " + m.Author.ID,
			})
			s.ChannelMessageSendEmbed(targetChannel.ID, &discordgo.MessageEmbed{
				Title: "🆕 New Ticket", Description: "User: " + m.Author.Mention(), Color: 0x3498db,
			})
		}

		embed := &discordgo.MessageEmbed{
			Author: &discordgo.MessageEmbedAuthor{Name: m.Author.Username, IconURL: m.Author.AvatarURL("")},
			Description: m.Content,
			Color: 0x2ecc71,
		}
		if len(m.Attachments) > 0 { embed.Image = &discordgo.MessageEmbedImage{URL: m.Attachments[0].URL} }

		s.ChannelMessageSendEmbed(targetChannel.ID, embed)
		logToDB(m.Author.ID, m.Content, "user", len(m.Attachments) > 0)
		return
	}

	// 2. STAFF -> USER
	// We first get the channel object to check the name and topic
	ch, err := s.Channel(m.ChannelID)
	if err != nil || ch.GuildID != GuildID || !strings.HasPrefix(ch.Name, "ticket-") {
		return
	}

	userID := ""
	if strings.HasPrefix(ch.Topic, "Modmail ID: ") {
		userID = strings.TrimPrefix(ch.Topic, "Modmail ID: ")
	}
	if userID == "" { return }

	// Handle !close
	if strings.ToLower(m.Content) == "!close" {
		s.ChannelDelete(m.ChannelID)
		dm, _ := s.UserChannelCreate(userID)
		s.ChannelMessageSend(dm.ID, "🔒 Your ticket has been closed.")
		return
	}

	// Forward message
	dm, err := s.UserChannelCreate(userID)
	if err != nil { return }

	embed := &discordgo.MessageEmbed{
		Title: "💬 Staff Response", Description: m.Content, Color: 0x3498db,
	}
	if len(m.Attachments) > 0 { embed.Image = &discordgo.MessageEmbedImage{URL: m.Attachments[0].URL} }

	_, err = s.ChannelMessageSendEmbed(dm.ID, embed)
	if err == nil {
		s.MessageReactionAdd(m.ChannelID, m.ID, "✅")
		logToDB(userID, m.Content, "staff", len(m.Attachments) > 0)
	}
}

func logToDB(uid, content, sender string, hasFile bool) {
	entry := ModmailLog{UserID: uid, Content: content, Timestamp: time.Now(), Sender: sender, HasFile: hasFile}
	_, _ = MsgCol.InsertOne(context.Background(), entry)
}
