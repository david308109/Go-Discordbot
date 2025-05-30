package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/TwiN/discord-music-bot/core"
	"github.com/TwiN/discord-music-bot/youtube"
	"github.com/bwmarrin/discordgo"
)

var (
	BotToken         = "YOUR_BOT_TOKEN" // 將這裡的 YOUR_BOT_TOKEN 替換為您的機器人 Token
	commandPrefix    = "!"              // 指令前綴
	MaximumQueueSize = 100              //最大駐列大小
	guilds           = make(map[string]*core.ActiveGuild)
	guildNames       = make(map[string]string)
	guildsMutex      = sync.RWMutex{}
	youtubeService   *youtube.Service

	// 聊天機器人設定
	app_name      = "YOUR_APP_NAME" // 將這裡的 YOUR_APP_NAME 替換為您的應用名稱
	user_id       = "user"
	session_id    = "YOUR_SESSION_ID"  // 將這裡的 YOUR_SESSION_ID 替換為您的會話 ID
	api_url       = "YOUR_URL/run_sse" // 將這裡的 YOUR_URL 替換為你開啟API的網域(http:// OR httpd:// + 網域名稱 + /run_sse)
	dc_channel_id = "YOUR_CHANNEL_ID"  // 將這裡的 YOUR_CHANNEL_ID 替換為你DC的頻道 ID

)

func main() {
	youtubeService = youtube.NewService(480)
	// 創建 Discord Session
	session, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		log.Fatal("error creating Discord bot,", err)
	}

	// 設定 intents
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuildMessages

	// 註冊消息處理器
	session.AddHandler(messageHandler)

	// 開啟與 Discord 的連接
	err = session.Open()
	if err != nil {
		log.Fatalf("error opening connection: %v", err)
	}

	defer session.Close()

	fmt.Println("the bot is online")

	// 捕獲終止信號以清理
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

// 訊息處理
func messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot || m.Author.ID == s.State.User.ID {
		return
	}

	log.Printf("收到訊息: %s, 用戶: %s, 文字頻道ID: %s", m.Content, m.Author.Username, m.ChannelID)

	if strings.HasPrefix(m.Content, commandPrefix) {
		command := strings.Replace(strings.Split(m.Content, " ")[0], commandPrefix, "", 1)
		query := strings.TrimSpace(strings.Replace(m.Content, fmt.Sprintf("%s%s", commandPrefix, command), "", 1))
		command = strings.ToLower(command)
		guildsMutex.Lock()
		activeGuild := guilds[m.GuildID]
		guildsMutex.Unlock()
		switch command {
		case "youtube", "yt", "play":
			HandleYoutubeCommand(s, activeGuild, m, query)
		case "skip":
			if activeGuild != nil && activeGuild.UserActions != nil {
				activeGuild.UserActions.Skip()
			}
		case "stop":
			if activeGuild != nil && activeGuild.UserActions != nil {
				activeGuild.UserActions.Stop()
			} else {
				// If queue is nil and the user still wrote !stop, it's possible that there's a VC still active
				s.Lock()
				if s.VoiceConnections[m.GuildID] != nil {
					log.Printf("[%s] Force disconnecting VC (!stop was called and queue was already nil)", GetGuildNameByID(s, m.GuildID))
					s.VoiceConnections[m.GuildID].Disconnect()
				}
				s.Unlock()
			}
		case "health":
			latency := s.HeartbeatLatency()
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Heartbeat latency: %s", latency))
		}
	}
	if m.ChannelID == dc_channel_id {
		guildsMutex.Lock()
		activeGuild := guilds[m.GuildID]
		guildsMutex.Unlock()
		text, err := Chat_Gemini(s, m)
		if err != "success" {
			log.Printf(text, err)
			_, _ = s.ChannelMessageSend(m.ChannelID, text+err)
		} else {
			messageContent := "<@" + m.Author.ID + ">" + " " + text
			_, _ = s.ChannelMessageSend(m.ChannelID, messageContent)
			HandleEdgeTTSCommand(s, activeGuild, m, text)
		}
	}
}

// 找用戶在哪個語音頻道
func GetVoiceChannelWhereMessageAuthorIs(s *discordgo.Session, m *discordgo.MessageCreate) (string, error) {
	// 獲取發送消息的用戶
	guild, err := s.State.Guild(m.GuildID)
	if err != nil {
		return "", err
	}
	for _, voiceState := range guild.VoiceStates {
		if voiceState.UserID == m.Author.ID {
			return voiceState.ChannelID, nil
		}
	}
	return "", errors.New("找不到語音頻道。")
}

// 取得伺服器名稱
func GetGuildNameByID(bot *discordgo.Session, guildID string) string {
	guildName, ok := guildNames[guildID]
	if !ok {
		guild, err := bot.Guild(guildID)
		if err != nil {
			// Failed to get the guild? Whatever, we'll just use the guild id
			guildNames[guildID] = guildID
			return guildID
		}
		guildNames[guildID] = guild.Name
		return guild.Name
	}
	return guildName
}

// joinVoiceChannelWithTimeout 加入語音頻道並設置超時退出
func joinVoiceChannelWithTimeout(s *discordgo.Session, guildID, channelID string, timeout time.Duration) (*discordgo.VoiceConnection, error) {
	voiceConn, err := s.ChannelVoiceJoin(guildID, channelID, false, false)
	if err != nil {
		return nil, err
	}

	// 建立一個完成信號通道
	done := make(chan struct{})

	go func() {
		timer := time.NewTimer(timeout) // 設定超時計時器
		defer timer.Stop()

		// 等待時間結束
		<-timer.C

		log.Println("超過指定時間，退出語音頻道。")
		voiceConn.Disconnect() // 自動退出語音頻道

		// 通知完成
		close(done)
	}()

	return voiceConn, nil
}

// 播放音樂
func HandleYoutubeCommand(s *discordgo.Session, activeGuild *core.ActiveGuild, m *discordgo.MessageCreate, query string) {
	if activeGuild != nil {
		if activeGuild.IsMediaQueueFull() {
			_, _ = s.ChannelMessageSend(m.ChannelID, "The queue is full!")
			return
		}
	} else {
		activeGuild = core.NewActiveGuild(GetGuildNameByID(s, m.GuildID))
		guildsMutex.Lock()
		guilds[m.GuildID] = activeGuild
		guildsMutex.Unlock()
	}
	// Find the voice channel the user is in
	voiceChannelId, err := GetVoiceChannelWhereMessageAuthorIs(s, m)
	if err != nil {
		log.Printf("[%s] Failed to find voice channel where message author is located: %s", activeGuild.Name, err.Error())
		_ = s.MessageReactionAdd(m.ChannelID, m.ID, "❌")
		_, _ = s.ChannelMessageSend(m.ChannelID, err.Error())
		return
	} else {
		log.Printf("[%s] Found user %s in voice channel %s", activeGuild.Name, m.Author.Username, voiceChannelId)
		_ = s.MessageReactionAdd(m.ChannelID, m.ID, "✅")
	}
	log.Printf("[%s] Searching for \"%s\"", activeGuild.Name, query)
	botMessage, _ := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(":mag: Searching for `%s`...", query))
	media, err := youtubeService.SearchAndDownload(query)
	if err != nil {
		log.Printf("[%s] Unable to find video for query \"%s\": %s", activeGuild.Name, query, err.Error())
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Unable to find video for query `%s`: %s", query, err.Error()))
		return
	}
	log.Printf("[%s] Successfully searched for and extracted audio from video with title \"%s\" to \"%s\"", activeGuild.Name, media.Title, media.FilePath)
	botMessage, _ = s.ChannelMessageEdit(botMessage.ChannelID, botMessage.ID, fmt.Sprintf(":white_check_mark: Found matching video titled `%s`!", media.Title))
	go func(s *discordgo.Session, m *discordgo.Message) {
		time.Sleep(5 * time.Second)
		_ = s.ChannelMessageDelete(botMessage.ChannelID, botMessage.ID)
	}(s, botMessage)
	// Add song to guild queue
	createNewWorker := false
	if !activeGuild.IsStreaming() {
		log.Printf("[%s] Preparing for streaming", activeGuild.Name)
		activeGuild.PrepareForStreaming(MaximumQueueSize)
		// If the channel was nil, it means that there was no worker
		createNewWorker = true
	}
	activeGuild.EnqueueMedia(media)
	log.Printf("[%s] Added media with title \"%s\" to queue at position %d", activeGuild.Name, media.Title, activeGuild.MediaQueueSize())
	_, _ = s.ChannelMessageSendEmbed(m.ChannelID, &discordgo.MessageEmbed{
		URL:         media.URL,
		Title:       media.Title,
		Description: fmt.Sprintf("Position in queue: %d", activeGuild.MediaQueueSize()),
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: media.Thumbnail,
		},
	})
	if createNewWorker {
		log.Printf("[%s] Starting worker", activeGuild.Name)
		go func() {
			err = worker(s, activeGuild, m.GuildID, voiceChannelId)

			if err != nil {
				log.Printf("[%s] Failed to start worker: %s", activeGuild.Name, err.Error())
				_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Unable to start voice worker: %s", err.Error()))
				_ = os.Remove(media.FilePath)
				return
			}
		}()
	}
}
