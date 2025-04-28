package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec" // 引入 os/exec 包用於執行外部命令
	"os/signal"
	"path/filepath" // 引入 path/filepath 處理檔案路徑
	"strings"
	"sync"
	"syscall"
	"time" // 引入 time 包用於生成臨時檔案名

	"github.com/TwiN/discord-music-bot/core"
	"github.com/TwiN/discord-music-bot/youtube"
	"github.com/bwmarrin/discordgo"
)

var (
	BotToken         = "YOUR_BOT_TOKEN" // 將這裡的 YOUR_BOT_TOKEN 替換為您的機器人 Token
	commandPrefix    = "!"                                                                        // 指令前綴
	MaximumQueueSize = 100                                                                        //最大駐列大小
	guilds           = make(map[string]*core.ActiveGuild)
	guildNames       = make(map[string]string)
	guildsMutex      = sync.RWMutex{}
	youtubeService   *youtube.Service

	// 聊天機器人設定
	app_name   = "YOUR_APP_NAME" // 將這裡的 YOUR_APP_NAME 替換為您的應用名稱
	// 注意：user_id 和 session_id 的獲取邏輯可能需要根據你的 API 設計調整
	// 在 Chat_Gemini 函式中會根據訊息來源動態獲取
	api_url    = "YOUR_URL/run_sse" // 將這裡的 YOUR_URL 替換為你開啟API的網域(http:// OR httpd:// + 網域名稱 + /run_sse)
	dc_channel_id = "YOUR_CHANNEL_ID" // 將這裡的 YOUR_CHANNEL_ID 替換為你DC的頻道 ID

)

func main() {
	youtubeService = youtube.NewService(480)
	// 創建 Discord Session
	session, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		log.Fatal("error creating Discord bot,", err)
	}

	// 設定 intents
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages // 添加 IntentsDirectMessages 如果你需要在私訊中使用 TTS

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
	// 忽略機器人自身的訊息和其他機器人的訊息
	if m.Author.Bot || m.Author.ID == s.State.User.ID {
		return
	}

	log.Printf("收到訊息 - 內容: %s, 用戶: %s, 文字頻道ID: %s, 伺服器ID: %s", m.Content, m.Author.Username, m.ChannelID, m.GuildID)

	// 處理帶有指令前綴的訊息 (保留原有的指令處理邏輯)
	if strings.HasPrefix(m.Content, commandPrefix) {
		// 分割指令和參數，只在第一個空格分割
		parts := strings.SplitN(m.Content, " ", 2)
		command := strings.Replace(parts[0], commandPrefix, "", 1)
		query := ""
		if len(parts) > 1 {
			query = strings.TrimSpace(parts[1])
		}
		command = strings.ToLower(command)

		// 獲取或初始化伺服器狀態 (如果訊息來自伺服器)
		var activeGuild *core.ActiveGuild = nil
		if m.GuildID != "" {
			guildsMutex.Lock()
			// 如果是新伺服器，可能需要在這裡初始化 guilds[m.GuildID] = &core.ActiveGuild{}
			activeGuild = guilds[m.GuildID]
			guildsMutex.Unlock()

            // 如果在伺服器內但 activeGuild 為 nil，可能是未進入語音頻道或其他未初始化情況
            // 對於音樂指令，如果 activeGuild 為 nil，通常不執行操作
            if activeGuild == nil && (command == "youtube" || command == "yt" || command == "play" || command == "skip" || command == "stop") {
                 log.Printf("收到伺服器音樂指令 (%s)，但伺服器狀態未初始化 (未進入語音頻道?) GuildID: %s", command, m.GuildID)
                 // 可以選擇在這裡發送提示訊息給使用者
                 // s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("請先將機器人加入語音頻道 (%sjoin)。", commandPrefix))
                 return // 不處理音樂相關指令
            }
		}


		// 根據指令內容執行相應操作
		switch command {
		case "youtube", "yt", "play":
             // 只有在伺服器內且 activeGuild 不為 nil 時處理音樂指令
			if m.GuildID != "" && activeGuild != nil {
				HandleYoutubeCommand(s, activeGuild, m, query) // 假設此函式存在
			} else if m.GuildID == "" {
                 s.ChannelMessageSend(m.ChannelID, "此命令僅在伺服器中可用。")
            }

		case "skip":
			if m.GuildID != "" && activeGuild != nil && activeGuild.UserActions != nil {
				activeGuild.UserActions.Skip() // 假設此方法存在 (來自 core.ActiveGuild)
			} else if m.GuildID == "" {
                 s.ChannelMessageSend(m.ChannelID, "此命令僅在伺服器中可用。")
            }
		case "stop":
			if m.GuildID != "" { // 停止指令在伺服器內才有意義
				if activeGuild != nil && activeGuild.UserActions != nil {
					activeGuild.UserActions.Stop() // 假設此方法存在 (來自 core.ActiveGuild)
				} else {
					// 如果 activeGuild 或 UserActions 為 nil，但機器人可能還在語音頻道中
					// 嘗試強制斷開語音連接
					s.Lock() // 鎖定 session 的 VoiceConnections map
					if s.VoiceConnections[m.GuildID] != nil {
						log.Printf("[%s] 偵測到 !stop 且無佇列，強制斷開語音連接。", GetGuildNameByID(s, m.GuildID)) // 假設 GetGuildNameByID 存在
						s.VoiceConnections[m.GuildID].Disconnect()
						// 清理伺服器狀態 (如果需要，例如從 guilds map 中刪除)
						guildsMutex.Lock()
						delete(guilds, m.GuildID) // 假設語音斷開時需要清理
						guildsMutex.Unlock()
					}
					s.Unlock()
				}
			} else {
                s.ChannelMessageSend(m.ChannelID, "此命令僅在伺服器中可用。")
            }

		case "health":
			latency := s.HeartbeatLatency()
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("心跳延遲 (Heartbeat latency): %s", latency))

		// ... 在這裡添加其他指令處理 ...
		}
	}

	// --- 處理發送到特定頻道 (dc_channel_id) 的訊息，用於觸發 Gemini TTS ---
	if m.ChannelID == dc_channel_id {
		log.Println("收到觸發 Gemini TTS 頻道的訊息")

		// 1. 調用 Chat_Gemini 處理訊息並獲取文字回應
		// Chat_Gemini 已經修改為回傳 string 和 error
		// 並且它會在內部處理錯誤訊息並發送到 Discord
		geminiResponseText, err := Chat_Gemini(s, m)
		if err != nil {
			// 如果 Chat_Gemini 回傳錯誤，表示處理 Gemini 請求失敗
			// Chat_Gemini 內部應該已經發送了錯誤提示給使用者
			log.Printf("Chat_Gemini 處理請求時發生錯誤: %v", err)
			// 在這裡不需重複發送錯誤訊息給使用者
			return // 發生錯誤，停止後續處理 (如 TTS)
		}

		// 2. 檢查 Gemini 是否回傳了有效的文字
		if geminiResponseText == "" {
			log.Println("Chat_Gemini 回傳了空回應文字。不進行 TTS 轉換。")
			// 如果回應為空，通常不需要做任何事，或者可以發送一個簡單提示
			// s.ChannelMessageSend(m.ChannelID, "我沒有得到有效的文字回應。")
			return // 回應為空，停止處理
		}

		// 3. 將獲取的文字回應轉換為語音並發送到 Discord
		// 調用新的 sendTextAsSpeech 函式
		err = sendTextAsSpeech(s, m.ChannelID, geminiResponseText)
		if err != nil {
			// 如果 sendTextAsSpeech 失敗 (例如 edge-tts 未安裝, 檔案權限問題等)
			log.Printf("將文字轉為語音或發送時發生錯誤: %v", err)
			// 通知使用者語音轉換失敗
			s.ChannelMessageSend(m.ChannelID, "對不起，將AI回應轉為語音時發生錯誤。")
			// 可選：如果語音轉換失敗，發送原始文字回應作為備用
			// s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("原始文字回應:\n%s", geminiResponseText))
		} else {
			// 語音成功發送，可以選擇不在發送原始文字，避免重複資訊
			log.Println("AI回應已成功轉換為語音並發送。")
		}
	}
}

// --- 以下是修改後的 Chat_Gemini 函式 ---

// Chat_Gemini 處理與 Gemini API 的互動，並回傳提取到的文字回應及可能的錯誤。
// 它在內部處理與 API 的通信和 JSON 解析。
// 成功時回傳提取到的文字和 nil 錯誤。
// 失敗時回傳空字串和錯誤，並會嘗試將錯誤訊息發送到 Discord 頻道。
func Chat_Gemini(s *discordgo.Session, m *discordgo.MessageCreate) (string, error) {
	// *** 根據你的 API 設計調整 user_id 和 session_id 的獲取 ***
	// 例如使用 Discord 用戶 ID 和頻道 ID 作為識別符
	currentUserID := m.Author.ID
	currentSessionID := m.ChannelID // 或者 m.GuildID + m.ChannelID 如果需要在伺服器內保持會話獨立

	data := map[string]any{
		"app_name": app_name,
		"user_id": currentUserID, // 使用實際用戶 ID
		"session_id": currentSessionID, // 使用實際會話 ID
		"new_message": map[string]any{
			// 標準的 Gemini API 使用 "user" 作為用戶角色
			"role": "user",
			"parts": []map[string]string{
				{
					"text": m.Content,
				},
			},
		},
		"streaming": false, // 我們不需要串流，因為我們一次處理完整回應來做 TTS
	}

	// 將數據編碼為 JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error marshalling JSON for Gemini API: %s", err.Error())
		// 通知使用者發生內部錯誤
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("對不起，處理你的請求時發生內部錯誤（JSON 編碼）。"))
		return "", fmt.Errorf("Error marshalling JSON for Gemini API: %w", err)
	}

	// 發送 POST 請求
	resp, err := http.Post(api_url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error sending POST request to Gemini API (%s): %s", api_url, err.Error())
		// 通知使用者無法聯繫到 API
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("對不起，無法聯繫到AI服務。"))
		return "", fmt.Errorf("Error sending POST request to %s: %w", api_url, err)
	}
	defer resp.Body.Close()

	// 檢查 HTTP 狀態碼，非 2xx 都視為錯誤
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        bodyBytes, _ := io.ReadAll(resp.Body) // 嘗試讀取錯誤響應體
        bodyString := string(bodyBytes)
        log.Printf("Gemini API returned non-2xx status code: %d. Body: %s", resp.StatusCode, bodyString)
        // 通知使用者 API 返回了錯誤狀態碼
        _, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("AI服務回傳了錯誤：%d", resp.StatusCode))
        return "", fmt.Errorf("Gemini API returned non-2xx status code: %d, body: %s", resp.StatusCode, bodyString)
    }


	// 讀取響應體
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading Gemini API response body: %s", err.Error())
		// 通知使用者讀取響應錯誤
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("對不起，讀取AI服務回應時發生錯誤。"))
		return "", fmt.Errorf("Error reading Gemini API response body: %w", err)
	}

	// 去掉 "data: " 前綴 (如果你的 API 回傳的格式是這樣，通常用於串流，非串流可能沒有)
	jsondata := []byte(strings.TrimPrefix(string(body), "data: "))
    log.Printf("Gemini API 原始響應體（可能移除data: 前綴）：%s", string(jsondata)) // 紀錄處理後的原始響應體

	// 使用 map 來解析 JSON 數據
	var result map[string]any
	// 根據你提供的 JSON 結構進行解析
	if err := json.Unmarshal([]byte(jsondata), &result); err != nil {
		log.Printf("Error decoding Gemini API JSON response: %s", err.Error())
		// 通知使用者 JSON 解析錯誤
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("對不起，解析AI服務回應時發生錯誤。"))
		return "", fmt.Errorf("Error decoding Gemini API JSON response: %w (data: %s)", err, string(jsondata))
	}

	// 提取 text 的內容
	// 根據你提供的解析路徑: result["content"]["parts"][0]["text"]
	if content, ok := result["content"].(map[string]any); ok {
		if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
			if part, ok := parts[0].(map[string]any); ok {
				if text, ok := part["text"].(string); ok {
					log.Printf("成功從 Gemini API 提取到 text: %s", text)
					// *** 成功提取到文字，回傳文字和 nil 錯誤 ***
					return text, nil
				}
			}
		}
	}

	// 如果上面的所有 if 條件都不滿足，表示未成功提取到 text
	log.Printf("未在 Gemini API 響應中找到 text 欄位。完整回應解析結果: %+v", result)
	// 通知使用者未找到文字內容
	_, _ = s.ChannelMessageSend(m.ChannelID, "對不起，AI服務回傳的回應中未包含文字內容。")
	// 回傳空字串和錯誤
	return "", fmt.Errorf("text field not found in Gemini API response. Parsed result: %+v", result)
}


// --- 以下是新的 sendTextAsSpeech 函式 ---

// sendTextAsSpeech 使用 edge-tts 將文字轉換為語音檔案，並將檔案發送到 Discord 頻道。
// 它處理 edge-tts 命令的執行和臨時檔案的管理。
func sendTextAsSpeech(s *discordgo.Session, channelID string, text string) error {
	// 創建一個獨特的臨時檔案名，用於儲存音訊檔案
	tempDir := os.TempDir() // 使用系統的臨時資料夾
	timestamp := time.Now().UnixNano()
	outputFileName := filepath.Join(tempDir, fmt.Sprintf("gemini_tts_%d.mp3", timestamp)) // 例如: /tmp/gemini_tts_1678881234567.mp3

	// edge-tts 命令及其參數
	// IMPORTANT: 請替換為你想要的語音。你可以執行 `edge-tts --list-voices` 來查看可用的語音並選擇。
	voice := "zh-TW-HsiaoChenNeural" // 台灣中文語音範例，請根據需要修改
	format := "audio-24khz-48kbitrate-mono-mp3" // 輸出格式範例，Discord 通常支援 MP3

	cmd := exec.Command("edge-tts",
		"--text", text,
		"--write-media", outputFileName,
		"--voice", voice,
		"--format", format,
	)

    log.Printf("執行 edge-tts 命令: %s", cmd.Args)
    // 可以在這裡選擇性地加入 `--log-level debug` 到 edge-tts 命令以獲取更多調試資訊

	// 執行命令並等待完成
	err := cmd.Run()
	if err != nil {
        // 檢查是否是因為找不到 edge-tts 命令或其他執行錯誤
        if exitError, ok := err.(*exec.ExitError); ok {
             // 嘗試讀取 stderr 輸出，這對於診斷 edge-tts 錯誤很有用
             // 注意：exitError.Stderr 可能是 nil
             stderrOutput := string(exitError.Stderr)
             log.Printf("edge-tts 命令執行失敗，退出碼: %d, stderr:\n%s", exitError.ExitCode(), stderrOutput)
             return fmt.Errorf("edge-tts 執行失敗 (退出碼 %d): %w (stderr: %s)", exitError.ExitCode(), err, stderrOutput)
        }
        // 其他類型的錯誤，例如命令找不到
		return fmt.Errorf("無法執行 edge-tts 命令: %w", err)
	}

	// 打開生成的音訊檔案
	audioFile, err := os.Open(outputFileName)
	if err != nil {
		return fmt.Errorf("無法打開生成的音訊檔案 %s: %w", outputFileName, err)
	}
	defer audioFile.Close() // 確保檔案在函式結束時被關閉

	// 在函式結束時刪除臨時檔案，無論成功或失敗
	defer func() {
		if removeErr := os.Remove(outputFileName); removeErr != nil {
			log.Printf("警告: 無法刪除臨時音訊檔案 %s: %v", outputFileName, removeErr)
		} else {
            log.Printf("成功刪除臨時音訊檔案 %s", outputFileName)
        }
	}()

	// 將檔案傳送到 Discord
	// discordgo.Session.ChannelFileSend 需要 channelID, name (Discord顯示的檔案名), reader
	// 使用 "gemini_response.mp3" 作為 Discord 中顯示的檔案名
	_, err = s.ChannelFileSend(channelID, "gemini_response.mp3", audioFile)
	if err != nil {
		return fmt.Errorf("無法將音訊檔案傳送到 Discord 頻道 %s: %w", channelID, err)
	}

    log.Printf("成功將音訊檔案傳送到頻道 %s", channelID)

	return nil // 成功完成
}


// --- 保留原有的其他輔助函式 (例如 GetVoiceChannelWhereMessageAuthorIs, GetGuildNameByID, HandleYoutubeCommand) ---
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
    if guildID == "" { // 處理私訊情況
        return "私訊頻道"
    }
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

// HandleYoutubeCommand 是你處理音樂播放的函式，這裡作為佔位符
// 確保你有引入 github.com/TwiN/discord-music-bot/core 和 github.com/TwiN/discord-music-bot/youtube
// 並且你的 ActiveGuild 結構體包含了 UserActions 和 MediaChan 等字段
func HandleYoutubeCommand(s *discordgo.Session, activeGuild *core.ActiveGuild, m *discordgo.MessageCreate, query string) {
	// 這裡是你原有的 HandleYoutubeCommand 函式內容
	// 請確保你的 core.ActiveGuild 結構體有實現 IsMediaQueueFull(), PrepareForStreaming(), EnqueueMedia(), MediaQueueSize(), IsStreaming()
	// 以及 UserActions 結構體及其 Skip() 方法
	if activeGuild != nil {
		if activeGuild.IsMediaQueueFull() {
			_, _ = s.ChannelMessageSend(m.ChannelID, "The queue is full!")
			return
		}
	} else {
		// 如果 activeGuild 不存在，可能需要在這裡初始化它，並將其加入 guilds map
		// 這通常發生在機器人加入語音頻道時，或者在處理第一個音樂指令時
		activeGuild = core.NewActiveGuild(GetGuildNameByID(s, m.GuildID)) // 假設 core.NewActiveGuild 存在
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
	// 假設 youtubeService.SearchAndDownload 存在並回傳 *core.Media 和 error
	media, err := youtubeService.SearchAndDownload(query) // 假設 youtubeService 已在 main 中初始化
	if err != nil {
		log.Printf("[%s] Unable to find video for query \"%s\": %s", activeGuild.Name, query, err.Error())
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Unable to find video for query `%s`: %s", query, err.Error()))
		// 清理可能下載的部分檔案
		if media != nil && media.FilePath != "" {
			_ = os.Remove(media.FilePath)
		}
		return
	}
	log.Printf("[%s] Successfully searched for and extracted audio from video with title \"%s\" to \"%s\"", activeGuild.Name, media.Title, media.FilePath)
	botMessage, _ = s.ChannelMessageEdit(botMessage.ChannelID, botMessage.ID, fmt.Sprintf(":white_check_mark: Found matching video titled `%s`!", media.Title))
	// 定時刪除搜尋訊息
	go func(s *discordgo.Session, m *discordgo.Message) {
		time.Sleep(5 * time.Second)
		_ = s.ChannelMessageDelete(botMessage.ChannelID, botMessage.ID)
	}(s, botMessage)
	// Add song to guild queue
	createNewWorker := false
	if !activeGuild.IsStreaming() { // 假設 IsStreaming() 存在
		log.Printf("[%s] Preparing for streaming", activeGuild.Name)
		activeGuild.PrepareForStreaming(MaximumQueueSize) // 假設 PrepareForStreaming 存在
		// If the channel was nil, it means that there was no worker
		createNewWorker = true
	}
	activeGuild.EnqueueMedia(media) // 假設 EnqueueMedia 存在
	log.Printf("[%s] Added media with title \"%s\" to queue at position %d", activeGuild.Name, activeGuild.MediaQueueSize()) // 假設 MediaQueueSize() 存在
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
            // 假設 worker 函式存在，並且它負責連接語音頻道和播放佇列中的媒體
			err = worker(s, activeGuild, m.GuildID, voiceChannelId) // 假設 worker 函式存在

			if err != nil {
				log.Printf("[%s] Failed to start worker: %s", activeGuild.Name, err.Error())
				_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Unable to start voice worker: %s", err.Error()))
				// 如果 worker 啟動失敗，需要清理所有已加入佇列但未播放的媒體檔案
				activeGuild.ClearMediaQueue() // 假設 ClearMediaQueue 存在
				// 清理語音連接狀態
				guildsMutex.Lock()
				delete(guilds, m.GuildID)
				guildsMutex.Unlock()
				return
			}
		}()
	}
}

// 注意：work.go 中的 worker 和 play 函式不需要修改，
// 它們處理的是音樂播放佇列。
// 只需要確保在 HandleYoutubeCommand 中正確調用 worker 即可。
