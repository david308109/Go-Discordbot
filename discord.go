package main

import (
    "errors"
    "fmt"
    "os"
    "os/exec"
    "os/signal"
    "strconv"
    "syscall"
    "sync"
    "github.com/bwmarrin/discordgo"
    "github.com/bwmarrin/discordgo/voice"
    "log"
    "time"
)

var (
    Token     = "MTM1OTAzNTYxNTQxNjQyMjQwMA.GJV6G3.hv1jhbcpfoyT1GIflSCubjKSkT1PMkSXI8Pr1w" // 將這裡替換為您的 Bot Token
    voiceConn *voice.VoiceConnection
    queue     []string             // 音樂隊列
    mu        sync.Mutex           // 鎖以保護隊列
    isPlaying = false              // 標記音樂是否正在播放
)

func main() {
    dg, err := discordgo.New("Bot " + Token)
    if err != nil {
        fmt.Println("error creating Discord session,", err)
        return
    }

    dg.AddMessageCreate(messageCreate)
    dg.AddHandler(ready)

    err = dg.Open()
    if err != nil {
        fmt.Println("error opening connection,", err)
        return
    }
    fmt.Println("Bot is now running. Press CTRL+C to exit.")

    // 等待關閉信號
    c := make(chan os.Signal, 1)
    signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
    <-c

    dg.Close()
}

func ready(s *discordgo.Session, event *discordgo.Ready) {
    fmt.Println("Bot is ready!")
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
    if m.Author.ID == s.State.User.ID {
        return // 忽略自己的消息
    }

    switch {
    case m.Content == "!join":
        joinVoiceChannel(s, m)
    case m.Content == "!leave":
        leaveVoiceChannel(s, m)
    case m.Content == "!queue":
        showQueue(s, m)
    case len(m.Content) > 6 && m.Content[:6] == "!play ":
        playYouTube(s, m)
    case len(m.Content) > 8 && m.Content[:8] == "!volume ":
        setVolume(s, m)
    default:
        return
    }
}

func joinVoiceChannel(s *discordgo.Session, m *discordgo.MessageCreate) {
    voiceChannelID := m.Member.VoiceChannelID
    if voiceChannelID == "" {
        s.ChannelMessageSend(m.ChannelID, "你需要在一個語音頻道中！")
        return
    }
    var err error
    voiceConn, err = s.ChannelVoiceJoin(m.GuildID, voiceChannelID, false, false)
    if err != nil {
        s.ChannelMessageSend(m.ChannelID, "無法加入語音頻道: "+err.Error())
        return
    }
    s.ChannelMessageSend(m.ChannelID, "已加入語音頻道！")
}

func leaveVoiceChannel(s *discordgo.Session, m *discordgo.MessageCreate) {
    if voiceConn != nil {
        voiceConn.Disconnect()
        voiceConn = nil
        isPlaying = false
        queue = nil // 清空隊列
        s.ChannelMessageSend(m.ChannelID, "已離開語音頻道！")
    }
}

func showQueue(s *discordgo.Session, m *discordgo.MessageCreate) {
    mu.Lock()
    defer mu.Unlock()

    if len(queue) == 0 {
        s.ChannelMessageSend(m.ChannelID, "隊列是空的！")
        return
    }
    response := "當前隊列:\n"
    for i, url := range queue {
        response += fmt.Sprintf("%d: %s\n", i+1, url)
    }
    s.ChannelMessageSend(m.ChannelID, response)
}

func playYouTube(s *discordgo.Session, m *discordgo.MessageCreate) {
    url := m.Content[6:] // 獲取 YouTube 連結
    mu.Lock()
    queue = append(queue, url)
    mu.Unlock()

    if !isPlaying {
        isPlaying = true
        go playNext(s)
    }
    s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("已將歌曲添加到隊列: %s", url))
}

func playNext(s *discordgo.Session) {
    mu.Lock()
    if len(queue) == 0 {
        isPlaying = false
        mu.Unlock()
        return
    }
    url := queue[0]
    queue = queue[1:] // 移除隊列中的第一首歌
    mu.Unlock()

    err := playYouTubeMusic(voiceConn, url)
    if err != nil {
        s.ChannelMessageSend("無法撥放音樂: " + err.Error())
    }

    // 播放完成後遞歸調用下一首
    playNext(s)
}

func setVolume(s *discordgo.Session, m *discordgo.MessageCreate) {
    volumeStr := m.Content[8:]
    volume, err := strconv.Atoi(volumeStr)
    if err != nil || volume < 0 || volume > 100 {
        s.ChannelMessageSend(m.ChannelID, "請提供 0 到 100 的有效音量值！")
        return
    }

    // 假設使用 ffmpeg 進行音量控制
    s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("音量已設置為 %d%%", volume))
    // 在這裡您應該將音量應用到音頻流中
    // 您可以根據具體情況進行修改
}

func playYouTubeMusic(vc *voice.VoiceConnection, url string) error {
    cmd := exec.Command("yt-dlp", "-f", "bestaudio", "--extract-audio", "--audio-format", "mp3", "--output", "-", url)

    pipe, err := cmd.StdoutPipe()
    if err != nil {
        return err
    }

    err = cmd.Start()
    if err != nil {
        return err
    }

    go func() {
        // 將音頻流傳送到 Discord
        buf := make([]byte, 1024)
        for {
            n, err := pipe.Read(buf)
            if err != nil {
                break
            }
            vc.OpusSend <- buf[:n]
        }
    }()

    // 等待命令執行完成
    err = cmd.Wait()
    if err != nil {
        log.Println("Error playing music:", err)
        return errors.New("無法撥放音樂，請檢查 YouTube 連結是否正確")
    }

    return nil
}
