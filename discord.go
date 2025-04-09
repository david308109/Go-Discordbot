package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	BotToken = "MTM1OTAzNTYxNTQxNjQyMjQwMA.GJV6G3.hv1jhbcpfoyT1GIflSCubjKSkT1PMkSXI8Pr1w" // 將這裡的 YOUR_BOT_TOKEN 替換為您的機器人 Token

	commandPrefix = "!" // 指令前綴

	guildsMutex = sync.RWMutex{}

	sc chan os.Signal
)

func main() {
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
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc
}

func messageHandler(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot || m.Author.ID == s.State.User.ID {
		return
	}

	log.Printf("收到訊息: %s, 用戶: %s", m.Content, m.Author.Username)

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

	// 如果收到 !join 命令，機器人加入語音頻道
	if m.Content == "!join" {
		if m.GuildID == "" {
			s.ChannelMessageSend(m.ChannelID, "需要在伺服器內使用此命令。")
			return
		}

		guild, err := s.State.Guild(m.GuildID)
		if err != nil || len(guild.VoiceStates) == 0 {
			s.ChannelMessageSend(m.ChannelID, "找不到語音頻道。")
			return
		}

		// 假設機器人加入第一個語音頻道
		voiceChannelID := guild.VoiceStates[0].ChannelID
		err = joinVoiceChannel(s, m.GuildID, voiceChannelID) // 使用 gID 和 cID
		if err != nil {
			log.Println("Error joining voice channel:", err)
			return
		}
		s.ChannelMessageSend(m.ChannelID, "已加入語音頻道。")
	}
	}
}

// joinVoiceChannel 函數加入語音頻道
func joinVoiceChannel(s *discordgo.Session, guildID string, voiceChannelID string) error {
	_, err := s.ChannelVoiceJoin(guildID, voiceChannelID, false, false) // 使用 gID 和 cID
	return err
}

// playMusic 函數播放音樂
func playMusic(s *discordgo.Session, guildID string, voiceChannelID string, url string) error {
	// 使用 yt-dlp 下載音樂流
	cmd := exec.Command("yt-dlp", "-f", "worstaudio", url) // 將每個參數單獨傳遞

	// 獲取標準輸出
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("獲取標準輸出失敗:%s ,網址: %s", err, url)
		return err
	}

	// 開始命令
	err = cmd.Start()
	if err != nil {
		log.Printf("開始命令失敗:%s ,網址: %s", err, url)
		return err
	}

	// 等待命令結束
	if err := cmd.Wait(); err != nil {
		log.Printf("等待命令結束失敗:%s ,網址: %s", err, url)
		return err
	}

	// 獲取語音連接
	voiceConnection, err := s.ChannelVoiceJoin(guildID, voiceChannelID, false, false)
	if err != nil {
		log.Printf("獲取語音連接失敗:%s ,網址: %s", err, url)
		return err
	}

	// 設置語音通道為正在播放狀態
	err = voiceConnection.Speaking(true)
	if err != nil {
		log.Printf("設置語音通道為正在播放狀態失敗:%s ,網址: %s", err, url)
		return err
	}

	// 將音頻流送入語音通道
	go func() {
		err = sendAudioToVoiceChannel(voiceConnection, pipe)
		if err != nil {
			log.Printf("將音頻流送入語音通道失敗:%s ,網址: %s", err, url)
		}
	}()

	return nil
}

// sendAudioToVoiceChannel 將音頻數據發送到語音通道
func sendAudioToVoiceChannel(voiceConnection *discordgo.VoiceConnection, pipe io.ReadCloser) error {
	buffer := make([]byte, 1024) // 每次讀取 1024 字節

	for {
		n, err := pipe.Read(buffer)
		if err != nil {
			break // 讀取錯誤或 EOF，退出循環
		}
		if n == 0 {
			break // 沒有更多數據時退出循環
		}

		// 將數據發送到 OpusSend channel
		voiceConnection.OpusSend <- buffer[:n] // 將數據發送到 OpusSend channel
	}

	return nil
}
