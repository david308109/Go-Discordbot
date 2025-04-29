package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/TwiN/discord-music-bot/core"
	"github.com/bwmarrin/discordgo"
)

func Chat_Gemini(s *discordgo.Session, m *discordgo.MessageCreate) (string, string) {
	data := map[string]any{
		"app_name":   app_name,
		"user_id":    user_id,
		"session_id": session_id,
		"new_message": map[string]any{
			"role": user_id,
			"parts": []map[string]string{
				{
					"text": m.Content,
				},
			},
		},
		"streaming": false, // 是否使用流式傳輸
	}

	// 將數據編碼為 JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return "Error marshalling JSON: ", err.Error()
	}

	// 發送 POST 請求
	resp, err := http.Post(api_url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "Error sending POST request: ", err.Error()
	}
	defer resp.Body.Close()

	// 打印響應狀態碼
	log.Printf("Response Status Code: %d", resp.StatusCode)

	// 打印響應頭
	log.Printf("Response Headers: %v", resp.Header)

	// 讀取響應體
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "Error reading response body: ", err.Error()
	}

	// 去掉 "data: " 前綴
	jsondata := []byte(strings.TrimPrefix(string(body), "data: "))

	// 使用 map 來解析 JSON 數據
	var result map[string]any
	if err := json.Unmarshal([]byte(jsondata), &result); err != nil {
		return "Error decoding JSON: ", err.Error()
	}

	// 提取 text 的內容
	if content, ok := result["content"].(map[string]any); ok {
		if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
			if part, ok := parts[0].(map[string]any); ok {
				if text, ok := part["text"].(string); ok {
					log.Printf("提取的 text: %s", text)
					return text, "success"
				}
			}
		}
	} else {
		log.Printf("未找到任何 text, 回傳值: %s", string(body))
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("未找到任何 text, 回傳值: %s", string(body)))
		return "未找到任何 text, 回傳值: ", string(body)
	}

	return "Error: ", "未找到任何 text"
}

// 使用 edge-tts-go 將文字轉換為語音檔案。
func sendTextAsSpeech(m *discordgo.MessageCreate, text string) (*core.Media, error) {
	tempDir := "data" // 儲存音訊的資料夾
	timestamp := time.Now().UnixNano()
	outputFileName := filepath.Join(tempDir, fmt.Sprintf("gemini_tts_%d.opus", timestamp))
	rate := "+0%"
	volume := "+0%"

	// edge-tts-go 命令及其參數
	voice := "zh-TW-HsiaoChenNeural" // 台灣中文語音範例，請根據需要修改

	cmd := exec.Command("edge-tts-go",
		"--text", text,
		"--write-media", outputFileName,
		"--voice", voice,
		"--rate", rate,
		"--volume", volume,
	)

	log.Printf("執行 edge-tts-go 命令: %s", cmd.Args)

	// 執行命令並等待完成
	err := cmd.Run()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			stderrOutput := string(exitError.Stderr)
			return nil, fmt.Errorf("edge-tts-go 執行失敗 (退出碼 %d): %w (stderr: %s)", exitError.ExitCode(), err, stderrOutput)
		}
		return nil, fmt.Errorf("無法執行 edge-tts-go 命令: %w", err)
	}

	Media := core.NewMedia(
		"AI語音擋",
		outputFileName,
		m.Author.ID,
		fmt.Sprintf("gemini_tts_%d", timestamp),
		outputFileName,
		int(10),
	)

	return Media, nil // 成功完成
}

// 播放音樂
func HandleEdgeTTSCommand(s *discordgo.Session, activeGuild *core.ActiveGuild, m *discordgo.MessageCreate, text string) {
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
	}
	media, err := sendTextAsSpeech(m, text)
	if err != nil {
		log.Printf("%s", err.Error())
		_, _ = s.ChannelMessageSend(m.ChannelID, err.Error())
		return
	}
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
