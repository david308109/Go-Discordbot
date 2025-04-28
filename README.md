## 程式碼變更建議

本節列出了針對 `github.com/TwiN/discord-music-bot` 專案的程式碼變更建議，旨在解決目前遇到的問題。

### 1. 解決 `failed to unmarshal video metadata` 錯誤

* **問題描述:**  
  程式在解析影片元數據時遇到錯誤，提示無法將 JSON 中的數字 `3.0` 反序列化到 Go 結構體 `videoMetadata` 中 `quality` 欄位的 `int` 類型。
* **影響檔案:**
    - `youtube.go` (位於 `github.com/TwiN/discord-music-bot/youtube` 目錄下)
* **建議修改行號:**
    - `youtube.go`：第 150 行
* **建議變更內容:**
  - 將 `Quality` 欄位的資料類型從 `int` 修改為 `float32` 或 `float64`，以適應可能出現的浮點數值。

    ```diff
    -       Quality int    `json:"quality"`
    +       Quality float32 `json:"quality"`
    ```

    **或**

    ```diff
    -       Quality int    `json:"quality"`
    +       Quality float64 `json:"quality"`
    ```

    **修改理由:**  
    影片元數據中的音質 (`quality`) 資訊可能以浮點數形式提供，將其類型更改為 `float32` 或 `float64` 可以避免反序列化錯誤，提高程式的健壯性。

---

### 2. 解決機器人加入語音頻道後無報錯但不播放音樂的問題

* **問題描述:**  
機器人可以順利加入語音頻道，但沒有播放音樂就直接斷開連接，且不會輸出任何錯誤訊息。  
經排查後，可能原因為：
    - 音量設定方式錯誤，導致 `ffmpeg` 啟動失敗但無明顯錯誤訊息。
    - 下載後檔案副檔名錯誤，導致播放時找不到正確檔案。

* **影響檔案:**
    - `encode.go`（位於 `github.com/TwiN/discord-music-bot/dca` 目錄下）
    - `youtube.go`（位於 `github.com/TwiN/discord-music-bot/youtube` 目錄下）

* **建議修改行號:**
    - `encode.go`：第 115 行
    - `youtube.go`：第 81 行

* **建議變更內容:**
  - I. 移除使用 `-vol` 傳遞音量參數，改用 `-af` 設定固定音量

    **原始程式碼:**
    ```go
    "-vol", strconv.Itoa(e.options.Volume),
    ```

    **替換為:**
    ```go
    "-af", "volume=0.5",
    ```

    **修改理由:**  
    由於沒有明確的錯誤訊息，推測可能是音量設定的處理方式導致了播放問題。  
    直接使用 `ffmpeg` 的 `volume` 音頻濾鏡並設定一個預設值（例如 `0.5`），可以簡化音量控制，避免因型別轉換錯誤或參數異常而造成播放失敗。若此方式確認穩定後，未來可以再擴充為可動態調整音量的機制。

  - II. 修正下載後檔案的副檔名錯誤

    **原始程式碼:**
    ```go
    videoMetadata.Filename,
    ```

    **替換為:**
    ```go
    strings.ReplaceAll(videoMetadata.Filename, ".webm", ".opus"),
    ```

    **修改理由:**  
    下載並轉檔後實際產生的音訊檔案副檔名為 `.opus`，但系統仍以 `.webm` 當作播放路徑，導致無法找到檔案。  
    在回傳媒體資訊時即修正檔名，可以確保播放時路徑正確，解決找不到音樂檔案的問題。

---

### 3. 解決收到指令卻沒有訊息內容的問題

* **問題描述:**  
  機器人有收到訊息事件，但 `MessageCreate` 中的 `Content` 欄位為空，導致無法處理指令。
* **建議變更內容:**
  - 確保在 Discord Developer Portal 中的 Bot 設定頁面中**啟用 `Message Content Intent`**。

    **補充說明:**  
    你有啟用 MessageContent Intent 嗎？從 2022 年起，Discord 要求你**明確啟用** Message Content Intent 才能讀取使用者的訊息內容。  

    前往 [Discord Developer Portal](https://discord.com/developers/applications)  
    選擇你的 Bot → 點選左側 **Bot** 頁籤 → 往下找到 **Privileged Gateway Intents** → 勾選 **Message Content Intent**。

    **修改理由:**  
    若未啟用該 Intent，Bot 將無法讀取任何文字訊息內容，即使收到事件也會導致 `m.Content` 為空值，這是造成指令無效的常見原因之一。

---

**後續步驟:**

請將以上建議的變更應用到您的程式碼中，並進行測試以驗證問題是否得到解決。
