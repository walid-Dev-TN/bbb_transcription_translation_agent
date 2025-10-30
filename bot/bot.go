package main

import (
        "crypto/sha1"
        "encoding/hex"
        "encoding/xml"
        "encoding/json"
        "fmt"
        "io/ioutil"
        "log"
        "math"
        "net"
        "net/http"
        "net/http/cookiejar"
        "net/url"
        "strconv"
        "strings"
        "sync"
        "time"
        "os"

        "github.com/go-rod/rod"
        "github.com/go-rod/rod/lib/launcher"
        "github.com/go-rod/rod/lib/proto"
        "github.com/google/uuid"
        "github.com/ysmood/gson"
        "golang.org/x/net/publicsuffix"
        "nhooyr.io/websocket"
)

// BrowserAudioCapturer handles audio capture via headless browser
type BrowserAudioCapturer struct {
        browser      *rod.Browser
        page         *rod.Page
        sessionToken string
        meetingID    string
        userID       string
        isConnected  bool
        audioConn    net.Conn // UDP connection for audio streaming
}

// NewBrowserAudioCapturer creates a new browser audio capturer
func NewBrowserAudioCapturer() *BrowserAudioCapturer {
        return &BrowserAudioCapturer{}
}

// BotManager manages multiple bots
type BotManager struct {
        Max_bots int

        lock sync.Mutex
        bots map[string]*Bot

        bbb_client_url         string
        bbb_client_ws          string
        bbb_pad_url            string
        bbb_pad_ws             string
        bbb_api_url            string
        bbb_api_secret         string
        bbb_webrtc_ws          string
        transcription_host     string
        transcription_port     int
        transcription_secret   string
        translation_server_url string
        changeset_external     bool
        changeset_port         int
        changeset_host         string
}

func NewBotManager(
        max_bots int,
        bbb_client_url string,
        bbb_client_ws string,
        bbb_pad_url string,
        bbb_pad_ws string,
        bbb_api_url string,
        bbb_api_secret string,
        bbb_webrtc_ws string,
        transcription_host string,
        transcription_port int,
        transcription_secret string,
        translation_server_url string,
        changeset_external bool,
        changeset_port int,
        changeset_host string,
) *BotManager {
        return &BotManager{
                Max_bots:               max_bots,
                bots:                   make(map[string]*Bot),
                bbb_client_url:         bbb_client_url,
                bbb_client_ws:          bbb_client_ws,
                bbb_pad_url:            bbb_pad_url,
                bbb_pad_ws:             bbb_pad_ws,
                bbb_api_url:            bbb_api_url,
                bbb_api_secret:         bbb_api_secret,
                bbb_webrtc_ws:          bbb_webrtc_ws,
                transcription_host:     transcription_host,
                transcription_port:     transcription_port,
                transcription_secret:   transcription_secret,
                translation_server_url: translation_server_url,
                changeset_external:     changeset_external,
                changeset_port:         changeset_port,
                changeset_host:         changeset_host,
        }
}

func (bm *BotManager) AddBot() (*Bot, error) {
        if len(bm.bots) >= bm.Max_bots {
                log.Printf("[ERROR] Max bots reached: %d", bm.Max_bots)
                return nil, fmt.Errorf("max bots reached: %d", bm.Max_bots)
        }

        new_bot := NewBot(
                bm.bbb_client_url,
                bm.bbb_client_ws,
                bm.bbb_pad_url,
                bm.bbb_pad_ws,
                bm.bbb_api_url,
                bm.bbb_api_secret,
                bm.bbb_webrtc_ws,
                bm.transcription_host,
                bm.transcription_port,
                bm.transcription_secret,
                bm.translation_server_url,
                bm.changeset_external,
                bm.changeset_port,
                bm.changeset_host,
                TaskTranscribe,
        )
        bm.lock.Lock()
        defer bm.lock.Unlock()

        bm.bots[new_bot.ID] = new_bot

        return new_bot, nil
}

func (bm *BotManager) RemoveBot(botID string) {
        bm.lock.Lock()
        defer bm.lock.Unlock()

        if bot, ok := bm.bots[botID]; ok {
                bot.Disconnect()
                delete(bm.bots, botID)
        }
}

func (bm *BotManager) Bot(botID string) (*Bot, bool) {
        bm.lock.Lock()
        defer bm.lock.Unlock()

        if bot, ok := bm.bots[botID]; ok {
                return bot, true
        }
        return nil, false
}

func (bm *BotManager) Bots() map[string]*Bot {
        bm.lock.Lock()
        defer bm.lock.Unlock()

        return bm.bots
}

// Task enum
type Task int

const (
        TaskTranscribe Task = iota
        TaskTranslate
)

// StatusType enum
type StatusType int

const (
        Connected StatusType = iota
        Connecting
        Disconnected
)

// XMLResponse represents the BBB API XML response
type XMLResponse struct {
        XMLName      xml.Name `xml:"response"`
        ReturnCode   string   `xml:"returncode"`
        MessageKey   string   `xml:"messageKey"`
        Message      string   `xml:"message"`
        MeetingID    string   `xml:"meeting_id"`
        UserID       string   `xml:"user_id"`
        AuthToken    string   `xml:"auth_token"`
        SessionToken string   `xml:"session_token"`
        URL          string   `xml:"url"`
        GuestStatus  string   `xml:"guestStatus"`
        VoiceBridge  string   `xml:"voiceBridge"`
}

// BBBClient handles BBB 3.0 connections
type BBBClient struct {
        MeetingID      string
        SessionToken   string
        UserID         string
        AuthToken      string
        WebSocketConn  *websocket.Conn
        IsConnected    bool
        apiURL         string
        apiSecret      string
        clientURL      string
        webrtcWS       string
        httpClient     *http.Client
        cookies        []*http.Cookie
        bot            *Bot
        VoiceBridge    string
        ConferenceName string
        consumers      map[string]interface{}
        audioProcessed bool
        audioApproved  bool
        audioJoinMutex sync.Mutex
}

func NewBBBClient(apiURL, apiSecret, clientURL, webrtcWS string, bot *Bot) *BBBClient {
        jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
        httpClient := &http.Client{
                Jar:     jar,
                Timeout: 30 * time.Second,
        }

        return &BBBClient{
                apiURL:         apiURL,
                apiSecret:      apiSecret,
                clientURL:      clientURL,
                webrtcWS:       webrtcWS,
                IsConnected:    false,
                httpClient:     httpClient,
                cookies:        []*http.Cookie{},
                bot:            bot,
                consumers:      make(map[string]interface{}),
                audioProcessed: false,
                audioApproved:  false,
        }
}

func (c *BBBClient) Leave() {
        c.IsConnected = false
        if c.WebSocketConn != nil {
                c.WebSocketConn.Close(websocket.StatusNormalClosure, "Leaving meeting")
        }

        // Close HTTP client connections
        if tr, ok := c.httpClient.Transport.(*http.Transport); ok {
                tr.CloseIdleConnections()
        }

        log.Printf("BBBClient disconnected from meeting %s", c.MeetingID)
}

func (c *BBBClient) generateChecksum(apiCall string, params string) string {
        data := apiCall + params + c.apiSecret
        hash := sha1.Sum([]byte(data))
        return hex.EncodeToString(hash[:])
}

func (c *BBBClient) JoinMeeting(meetingID, moderatorPW, userName string) error {
        params := url.Values{}
        params.Add("meetingID", meetingID)
        params.Add("fullName", userName)
        params.Add("joinViaHtml5", "true")
        params.Add("redirect", "false")
        params.Add("role", "MODERATOR")
        if moderatorPW != "" {
                params.Add("password", moderatorPW)
        }
        params.Add("listenOnly", "false")
        params.Add("avatarURL", "")
        params.Add("clientURL", c.clientURL)

        apiCall := "join"
        queryString := params.Encode()
        checksum := c.generateChecksum(apiCall, queryString)

        joinURL := fmt.Sprintf("%sjoin?%s&checksum=%s", c.apiURL, queryString, checksum)

        log.Printf("DEBUG: Joining meeting: %s", joinURL)

        resp, err := c.httpClient.Get(joinURL)
        if err != nil {
                return fmt.Errorf("failed to join meeting: %v", err)
        }
        defer resp.Body.Close()

        body, err := ioutil.ReadAll(resp.Body)
        if err != nil {
                return fmt.Errorf("failed to read response: %v", err)
        }

        c.cookies = resp.Cookies()

        var xmlResponse XMLResponse
        if err := xml.Unmarshal(body, &xmlResponse); err == nil {
                if xmlResponse.ReturnCode == "FAILED" {
                        return fmt.Errorf("join failed: %s - %s", xmlResponse.MessageKey, xmlResponse.Message)
                }
                if xmlResponse.ReturnCode == "SUCCESS" {
                        if xmlResponse.SessionToken == "" {
                                return fmt.Errorf("no session token received")
                        }

                        c.SessionToken = xmlResponse.SessionToken
                        c.MeetingID = xmlResponse.MeetingID
                        c.UserID = xmlResponse.UserID
                        c.VoiceBridge = xmlResponse.VoiceBridge
                        c.AuthToken = xmlResponse.AuthToken

                        if c.VoiceBridge == "" {
                                if voiceBridge, err := c.getVoiceBridgeFromAPI(c.MeetingID, moderatorPW); err == nil {
                                        c.VoiceBridge = voiceBridge
                                } else {
                                        parts := strings.Split(c.MeetingID, "-")
                                        if len(parts) > 1 {
                                                timestamp := parts[len(parts)-1]
                                                if len(timestamp) >= 5 {
                                                        c.VoiceBridge = timestamp[len(timestamp)-5:]
                                                }
                                        }

                                        if c.VoiceBridge == "" {
                                                hash := sha1.Sum([]byte(c.MeetingID))
                                                c.VoiceBridge = fmt.Sprintf("%05d", (int(hash[0])*1000+int(hash[1])*10+int(hash[2])%10)%10000)
                                        }
                                }
                        }

                        log.Printf("Joined meeting. Session: %s, User ID: %s, Voice Bridge: %s",
                                c.SessionToken, c.UserID, c.VoiceBridge)

                        return nil
                }
        }

        return fmt.Errorf("failed to parse join response: %s", string(body))
}

func (c *BBBClient) getVoiceBridgeFromAPI(meetingID, moderatorPW string) (string, error) {
        params := url.Values{}
        params.Add("meetingID", meetingID)
        if moderatorPW != "" {
                params.Add("password", moderatorPW)
        }

        apiCall := "getMeetingInfo"
        queryString := params.Encode()
        checksum := c.generateChecksum(apiCall, queryString)

        apiURL := fmt.Sprintf("%sgetMeetingInfo?%s&checksum=%s", c.apiURL, queryString, checksum)

        resp, err := http.Get(apiURL)
        if err != nil {
                return "", err
        }
        defer resp.Body.Close()

        body, err := ioutil.ReadAll(resp.Body)
        if err != nil {
                return "", err
        }

        // Parse XML response
        var response struct {
                XMLName     xml.Name `xml:"response"`
                ReturnCode  string   `xml:"returncode"`
                VoiceBridge string   `xml:"voiceBridge"`
        }

        if err := xml.Unmarshal(body, &response); err != nil {
                return "", err
        }

        if response.ReturnCode != "SUCCESS" || response.VoiceBridge == "" {
                return "", fmt.Errorf("voiceBridge not found in API response")
        }

        return response.VoiceBridge, nil
}

// BrowserAudioCapturer Methods
func (bac *BrowserAudioCapturer) setupBrowser() error {
        // Force rod to use the system Chrome instead of downloading
        u := launcher.New().
                Headless(true).
                Devtools(false).
                Bin("/usr/bin/chromium-browser").
                Set("disable-gpu", "true").
                Set("no-sandbox", "true").
                Set("disable-dev-shm-usage", "true").
                MustLaunch()

        bac.browser = rod.New().ControlURL(u).MustConnect()

        page, err := bac.browser.Page(proto.TargetCreateTarget{})
        if err != nil {
                return err
        }
        bac.page = page

        bac.page.MustSetViewport(1280, 720, 1.0, false)

        return nil
}

func (bac *BrowserAudioCapturer) joinBBBMeeting(meetingID, moderatorPW, userName string) error {
        // Use the Greenlight join URL (from .env file)
        joinURL := os.Getenv("BBB_JOIN_BASE_URL")
        log.Printf("Navigating to Greenlight join URL: %s", joinURL)
        bac.page.MustNavigate(joinURL)
        bac.page.MustWaitLoad()

        // Set a larger viewport to ensure all elements are visible
        bac.page.MustSetViewport(1920, 1080, 1.0, false)

        return nil
}

func (bac *BrowserAudioCapturer) setupAudioStreaming(host string, port int) error {
        log.Printf("🚨 DEBUG: Setting up AUDIO streaming to %s:%d using UDP", host, port)

        conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", host, port))
        if err != nil {
                log.Printf("❌ UDP connection failed: %v", err)
                return fmt.Errorf("failed to connect to audio port: %v", err)
        }

        log.Printf("✅ UDP audio connection established to %s:%d", host, port)

        // Test with a larger packet
        testData := make([]byte, 4096)
        for i := range testData {
                testData[i] = byte(i % 256)
        }

        n, err := conn.Write(testData)
        if err != nil {
                log.Printf("❌ UDP test packet failed: %v", err)
        } else {
                log.Printf("✅ UDP test packet sent: %d bytes", n)
        }

        bac.audioConn = conn
        return nil
}

func (bac *BrowserAudioCapturer) handleBBBJoinFlow() {
        log.Printf("Handling Greenlight join flow...")

        // Wait for the React app to load
        log.Printf("Waiting for Greenlight React app to load...")
        time.Sleep(10 * time.Second)
        bac.page.MustWaitIdle()

        // Debug: Check what's on the page
        bac.debugPageContent()

        // Step 1: Fill out the name field
        log.Printf("Filling out participant name")

        nameSelectors := []string{
                "input[placeholder*='name']",
                "input[placeholder*='Name']",
                "input[type='text']:not([type='checkbox'])",
                "input:first-of-type",
                "input",
        }

        var nameInput *rod.Element
        for _, selector := range nameSelectors {
                nameInput, _ = bac.page.Element(selector)
                if nameInput != nil {
                        if visible, _ := nameInput.Visible(); visible {
                                nameInput.MustInput("Bot")
                                log.Printf("Filled name field using selector: %s", selector)
                                break
                        }
                }
        }

        // Step 2: Check the acknowledgement checkbox
        log.Printf("Checking acknowledgement checkbox")
        checkboxes, err := bac.page.Elements("input[type='checkbox']")
        if err == nil && len(checkboxes) > 0 {
                checkbox := checkboxes[0]
                if visible, _ := checkbox.Visible(); visible {
                        checkbox.MustScrollIntoView()
                        checkbox.MustClick()
                        log.Printf("Checked acknowledgement box")
                        time.Sleep(1 * time.Second)
                }
        } else {
                log.Printf("No checkbox found, trying to find second input")
                allInputs, _ := bac.page.Elements("input")
                if len(allInputs) >= 2 {
                        secondInput := allInputs[1]
                        if visible, _ := secondInput.Visible(); visible {
                                secondInput.MustClick()
                                log.Printf("Clicked second input (assuming it's checkbox)")
                                time.Sleep(1 * time.Second)
                        }
                }
        }

        // Step 3: Click the "Join Meeting" button
        log.Printf("Clicking join meeting button")

        buttonSelectors := []string{
                "button:contains('Join Meeting')",
                "button",
                "input[type='submit']",
                "[type='submit']",
        }

        var joinButton *rod.Element
        for _, selector := range buttonSelectors {
                joinButton, err = bac.page.Element(selector)
                if err == nil && joinButton != nil {
                        if visible, _ := joinButton.Visible(); visible {
                                joinButton.MustScrollIntoView()
                                joinButton.MustClick()
                                log.Printf("Clicked join button using selector: %s", selector)
                                break
                        }
                }
        }

        if joinButton == nil {
                log.Printf("Join button not found despite debug showing it exists")
                // Fallback: try to press Enter
                bac.page.Keyboard.Press('\r')
                log.Printf("Pressed Enter key as fallback")
        }

        // Wait for the BBB HTML5 client to load
        log.Printf("Waiting for BBB client to load...")
        time.Sleep(12 * time.Second)

        // Debug BBB client page
        bac.debugPageContent()

        // Step 4: Choose "Listen only" mode
        log.Printf("Selecting listen only mode in BBB client")

        listenOnlySelectors := []string{
                "button[aria-label*='Listen only']",
                "div[aria-label*='Listen only']",
                "[data-test='listenOnly']",
                "button:contains('Listen only')",
                "div:contains('Listen only')",
        }

        var listenOnlyBtn *rod.Element
        for _, selector := range listenOnlySelectors {
                element, err := bac.page.Element(selector)
                if err == nil && element != nil {
                        if visible, _ := element.Visible(); visible {
                                listenOnlyBtn = element
                                log.Printf("Found listen only button with selector: %s", selector)
                                break
                        }
                }
        }

        if listenOnlyBtn != nil {
                listenOnlyBtn.MustScrollIntoView()
                listenOnlyBtn.MustClick()
                log.Printf("Clicked listen only button")
        } else {
                log.Printf("Listen only button not found, trying fallback...")
                // Press Enter as final fallback
                time.Sleep(2 * time.Second)
                bac.page.Keyboard.Press('\r')
                log.Printf("Pressed Enter key as final fallback")
        }

        log.Printf("Greenlight + BBB join flow completed")
        time.Sleep(5 * time.Second)

        log.Printf("Analyzing BBB environment...")
        bac.debugBBBEnvironment()

        // Wait for BBB to fully initialize
        time.Sleep(8 * time.Second)

        log.Printf("BBB join flow fully completed")
}

func (bac *BrowserAudioCapturer) captureBBBAudioWorkaround() error {
    log.Printf("Setting up BBB audio workaround...")

    jsCode := `() => {
        console.log('[BBB-Bot] Starting BBB audio workaround');

        // Wait for BBB to fully load
        return new Promise((resolve) => {
            const checkBBBReady = () => {
                // Look for specific BBB UI elements that indicate audio is active
                const audioIndicators = [
                    document.querySelector('[data-test="audioBtn"]'),
                    document.querySelector('.audio-controls'),
                    document.querySelector('[aria-label*="audio"]'),
                    document.querySelector('[title*="audio"]')
                ].filter(Boolean);

                if (audioIndicators.length > 0) {
                    console.log('BBB audio UI detected, starting capture');
                    startAudioCapture();
                    resolve({ success: true, method: 'ui_detection' });
                    return;
                }

                // Alternative: Check for HTML5 audio elements created by BBB
                const audioElements = document.querySelectorAll('audio');
                let hasPlayingAudio = false;

                audioElements.forEach((audio, idx) => {
                    if (!audio.paused && !audio.muted && audio.readyState > 0) {
                        console.log('Found playing audio element:', idx);
                        hookIntoAudioElement(audio);
                        hasPlayingAudio = true;
                    }
                });

                if (hasPlayingAudio) {
                    resolve({ success: true, method: 'audio_elements' });
                    return;
                }

                // Keep checking
                console.log('BBB not fully ready, waiting...');
                setTimeout(checkBBBReady, 2000);
            };

            function hookIntoAudioElement(audio) {
                console.log('Hooking into audio element');

                try {
                    const audioContext = new (window.AudioContext || window.webkitAudioContext)({
                        sampleRate: 16000
                    });

                    const source = audioContext.createMediaElementSource(audio);
                    const processor = audioContext.createScriptProcessor(2048, 1, 1);

                    source.connect(processor);
                    processor.connect(audioContext.destination);

                    let packetCount = 0;

                    processor.onaudioprocess = function(event) {
                        const inputData = event.inputBuffer.getChannelData(0);

                        // 🆕 FIXED: Check for non-silent audio with LOWER threshold
                        let hasAudio = false;
                        let maxAmplitude = 0;
                        for (let i = 0; i < inputData.length; i++) {
                            const amplitude = Math.abs(inputData[i]);
                            if (amplitude > 0.0001) {  // 🆕 LOWERED from 0.01 to 0.0001
                                hasAudio = true;
                                maxAmplitude = Math.max(maxAmplitude, amplitude);
                            }
                        }

                        // 🆕 FIXED: Process all audio, don't skip based on threshold
                        // Only skip if completely silent
                        let rms = 0;
                        for (let i = 0; i < inputData.length; i++) {
                            rms += inputData[i] * inputData[i];
                        }
                        rms = Math.sqrt(rms / inputData.length);

                        if (rms < 0.00005) {  // 🆕 Only skip if virtually silent
                            return;
                        }

                        const pcmData = new Int16Array(inputData.length);
                        for (let i = 0; i < inputData.length; i++) {
                            pcmData[i] = Math.max(-32768, Math.min(32767, inputData[i] * 32768));
                        }

                        const byteArray = new Uint8Array(pcmData.buffer);

                        if (window.sendAudioData) {
                            window.sendAudioData(Array.from(byteArray));
                            packetCount++;

                            if (packetCount % 100 === 0) {
                                console.log('Audio workaround packet:', packetCount, 'rms:', rms.toFixed(6));
                            }
                        }
                    };

                    console.log('Successfully hooked into audio element');

                } catch (error) {
                    console.error('Error hooking into audio element:', error);
                }
            }

            function startAudioCapture() {
                // Try to find the actual audio stream
                const findAudioStream = () => {
                    // Method 1: Look for MediaStreams in global objects
                    for (let key in window) {
                        try {
                            const obj = window[key];
                            if (obj instanceof MediaStream) {
                                console.log('Found MediaStream:', key);
                                if (obj.getAudioTracks().length > 0) {
                                    console.log('Stream has audio tracks');
                                    processAudioStream(obj);
                                }
                            }
                        } catch (e) {
                            // Skip inaccessible properties
                        }
                    }

                    // Method 2: Monitor for new audio elements
                    const observer = new MutationObserver((mutations) => {
                        mutations.forEach((mutation) => {
                            mutation.addedNodes.forEach((node) => {
                                if (node.nodeName === 'AUDIO' && !node.processed) {
                                    console.log('New audio element detected');
                                    setTimeout(() => hookIntoAudioElement(node), 1000);
                                }
                            });
                        });
                    });

                    observer.observe(document.body, {
                        childList: true,
                        subtree: true
                    });
                };

                findAudioStream();
            }

            function processAudioStream(stream) {
                console.log('Processing audio stream:', stream.id);

                if (stream.processed) return;
                stream.processed = true;

                try {
                    const audioContext = new (window.AudioContext || window.webkitAudioContext)({
                        sampleRate: 16000
                    });

                    const source = audioContext.createMediaStreamSource(stream);
                    const processor = audioContext.createScriptProcessor(2048, 1, 1);

                    source.connect(processor);
                    processor.connect(audioContext.destination);

                    let packetCount = 0;
                    let lastLogTime = Date.now();

                    processor.onaudioprocess = function(event) {
                        const inputData = event.inputBuffer.getChannelData(0);

                        // 🆕 FIXED: Check for actual audio with LOWER threshold
                        let hasAudio = false;
                        let maxAmplitude = 0;
                        let rms = 0;
                        for (let i = 0; i < inputData.length; i++) {
                            const amplitude = Math.abs(inputData[i]);
                            rms += amplitude * amplitude;
                            if (amplitude > 0.0001) {  // 🆕 LOWERED from 0.001 to 0.0001
                                hasAudio = true;
                                maxAmplitude = Math.max(maxAmplitude, amplitude);
                            }
                        }

                        rms = Math.sqrt(rms / inputData.length);

                        // 🆕 FIXED: Only skip if completely silent
                        if (rms < 0.00005) {
                            return;
                        }

                        const pcmData = new Int16Array(inputData.length);
                        for (let i = 0; i < inputData.length; i++) {
                            pcmData[i] = Math.max(-32768, Math.min(32767, inputData[i] * 32768));
                        }

                        const byteArray = new Uint8Array(pcmData.buffer);

                        if (window.sendAudioData) {
                            console.log('🎵 Audio processor: Calling sendAudioData with', byteArray.length, 'bytes, amplitude:', maxAmplitude.toFixed(6));
                            window.sendAudioData(Array.from(byteArray));
                            packetCount++;

                            if (packetCount % 50 === 0 || Date.now() - lastLogTime > 5000) {
                                console.log('Audio packet', packetCount, 'bytes:', byteArray.length, 'amp:', maxAmplitude.toFixed(6), 'rms:', rms.toFixed(6));
                                lastLogTime = Date.now();
                            }
                        }
                    };

                    console.log('Successfully processing audio stream:', stream.id);

                } catch (error) {
                    console.error('Error processing audio stream:', error);
                }
            }

            // Start checking
            checkBBBReady();
        });
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("BBB audio workaround failed: %v", err)
        return err
    }

    log.Printf("BBB Audio Workaround: %v", result)
    return nil
}


func (bac *BrowserAudioCapturer) captureBBBFreeSWITCHAudio() error {
    log.Printf("Attempting to capture via BBB's FreeSWITCH audio bridge...")

    jsCode := `() => {
        console.log('[BBB-Bot] Targeting BBB FreeSWITCH audio bridge');

        // BBB uses FreeSWITCH for audio processing - let's find the bridge
        function findAudioBridgeComponents() {
            console.log('Searching for FreeSWITCH audio bridge components...');

            // Common BBB audio bridge objects
            const bridgeTargets = [
                'freeswitch',
                'audioBridge',
                'sipBridge',
                'voiceBridge',
                'audioSession',
                'voiceSession',
                'conferenceAudio'
            ];

            let bridgeComponents = {};

            // Search in various scopes
            const searchScopes = [window, window.App, window.APP, window.BBB];

            searchScopes.forEach(scope => {
                if (!scope) return;

                bridgeTargets.forEach(target => {
                    if (scope[target]) {
                        const scopeName = scope === window ? 'window' :
                                        scope === window.App ? 'App' :
                                        scope === window.APP ? 'APP' : 'BBB';
                        bridgeComponents[scopeName + '.' + target] = {
                            exists: true,
                            type: typeof scope[target],
                            value: scope[target] ? 'EXISTS' : 'null'
                        };
                        console.log('Found bridge component:', scopeName + '.' + target);
                    }
                });
            });

            return bridgeComponents;
        }

        // Strategy: Hook into the audio stream before it goes to FreeSWITCH
        function hookIntoPreBridgeAudio() {
            console.log('Attempting to hook into pre-bridge audio...');

            // Method 1: Look for the WebRTC to SIP conversion point
            if (window.App && window.App.audio && window.App.audio.addStream) {
                console.log('Found App.audio.addStream - hooking in...');
                const originalAddStream = window.App.audio.addStream;
                window.App.audio.addStream = function(stream, userId) {
                    console.log('🎵 App.audio.addStream called for user:', userId);
                    console.log('Stream details:', {
                        id: stream.id,
                        active: stream.active,
                        audioTracks: stream.getAudioTracks().length
                    });

                    // Process this stream (this is likely the bridge input)
                    processBridgeAudioStream(stream, 'App.audio.addStream');

                    return originalAddStream.call(this, stream, userId);
                };
            }

            // Method 2: Look for conference audio handlers
            if (window.APP && window.APP.conference && window.APP.conference.audioStream) {
                console.log('Found APP.conference.audioStream');
                const audioStream = window.APP.conference.audioStream;
                if (audioStream.addStream) {
                    const originalAddStream = audioStream.addStream;
                    audioStream.addStream = function(stream) {
                        console.log('🎵 APP.conference.audioStream.addStream called');
                        processBridgeAudioStream(stream, 'APP.conference.audioStream');
                        return originalAddStream.call(this, stream);
                    };
                }
            }

            // Method 3: Monitor for any audio stream additions
            if (window.MediaStream && window.MediaStream.prototype.addTrack) {
                const originalAddTrack = window.MediaStream.prototype.addTrack;
                window.MediaStream.prototype.addTrack = function(track) {
                    console.log('MediaStream.addTrack:', track.kind, track.id);
                    if (track.kind === 'audio') {
                        console.log('🎵 Audio track added to MediaStream');
                        setTimeout(() => {
                            if (this.getAudioTracks().includes(track)) {
                                processBridgeAudioStream(this, 'MediaStream.addTrack');
                            }
                        }, 100);
                    }
                    return originalAddTrack.call(this, track);
                };
            }
        }

        function processBridgeAudioStream(stream, source) {
            console.log('Processing bridge audio stream from:', source, stream.id);

            if (stream.processed) return;
            stream.processed = true;

            try {
                const audioContext = new (window.AudioContext || window.webkitAudioContext)({
                    sampleRate: 16000
                });

                const sourceNode = audioContext.createMediaStreamSource(stream);
                const processor = audioContext.createScriptProcessor(2048, 1, 1);

                sourceNode.connect(processor);
                processor.connect(audioContext.destination);

                let packetCount = 0;

                processor.onaudioprocess = function(event) {
                    const inputData = event.inputBuffer.getChannelData(0);

                    // 🆕 FIXED: Check for actual audio with LOWER threshold
                    let hasAudio = false;
                    let maxAmplitude = 0;
                    let rms = 0;
                    for (let i = 0; i < inputData.length; i++) {
                        const amplitude = Math.abs(inputData[i]);
                        rms += amplitude * amplitude;
                        if (amplitude > 0.0001) {  // 🆕 LOWERED from 0.005 to 0.0001
                            hasAudio = true;
                            maxAmplitude = Math.max(maxAmplitude, amplitude);
                        }
                    }

                    rms = Math.sqrt(rms / inputData.length);

                    // 🆕 FIXED: Only skip if completely silent
                    if (rms < 0.00005) {
                        return;
                    }

                    const pcmData = new Int16Array(inputData.length);
                    for (let i = 0; i < inputData.length; i++) {
                        pcmData[i] = Math.max(-32768, Math.min(32767, inputData[i] * 32768));
                    }

                    const byteArray = new Uint8Array(pcmData.buffer);

                    if (window.sendAudioData) {
                        window.sendAudioData(Array.from(byteArray));
                        packetCount++;

                        if (packetCount % 25 === 0) {
                            console.log('Bridge audio packet:', packetCount, 'source:', source, 'amp:', maxAmplitude.toFixed(6), 'rms:', rms.toFixed(6));
                        }
                    }
                };

                console.log('✅ Successfully hooked into bridge audio stream from:', source);

            } catch (error) {
                console.error('Error processing bridge audio stream:', error);
            }
        }

        // Method 4: Direct FreeSWITCH bridge access attempt
        function attemptDirectBridgeAccess() {
            console.log('Attempting direct FreeSWITCH bridge access...');

            // Look for the voice bridge number (from the docs)
            const voiceBridgeElement = document.querySelector('[data-test="voiceBridge"]') ||
                         document.querySelector('[aria-label*="bridge"]') ||
                         document.querySelector('[title*="bridge"]') ||
                         document.querySelector('.voice-bridge') ||
                         document.querySelector('.conference-bridge');

            if (voiceBridgeElement) {
                console.log('Found voice bridge element:', voiceBridgeElement);
                console.log('Voice bridge content:', voiceBridgeElement.textContent);
            }

            // Check if we're in a voice bridge session
            if (window.APP && window.APP.conference && window.APP.conference.room) {
                const room = window.APP.conference.room;
                console.log('Conference room voice info:', {
                    hasVoiceBridge: !!room.voiceBridge,
                    voiceBridge: room.voiceBridge,
                    hasAudioConsumers: room._audioConsumers ? room._audioConsumers.size : 0
                });
            }
        }

        // Execute all strategies
        const components = findAudioBridgeComponents();
        hookIntoPreBridgeAudio();
        attemptDirectBridgeAccess();

        // Also process any existing streams
        setTimeout(() => {
            console.log('Checking for existing audio streams...');
            const existingStreams = [];

            // Find all MediaStreams
            for (let key in window) {
                try {
                    if (window[key] instanceof MediaStream) {
                        const stream = window[key];
                        if (stream.getAudioTracks().length > 0) {
                            console.log('Found existing MediaStream with audio:', key);
                            existingStreams.push(stream);
                        }
                    }
                } catch (e) {}
            }

            existingStreams.forEach(stream => {
                processBridgeAudioStream(stream, 'existing_stream_' + stream.id);
            });
        }, 2000);

        return {
            strategy: 'freeswitch_bridge_capture',
            components: components,
            status: 'bridge_monitoring_active'
        };
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("FreeSWITCH bridge capture failed: %v", err)
        return err
    }

    log.Printf("FreeSWITCH Bridge Capture: %v", result)
    return nil
}


func (bac *BrowserAudioCapturer) debugAudioConversion() error {
    log.Printf("Debugging JavaScript audio conversion...")

    jsCode := `() => {
        console.log('=== AUDIO CONVERSION DEBUG ===');

        // Test the PCM conversion process
        const testInput = new Float32Array([0.1, -0.2, 0.05, -0.3, 0.15]);
        console.log('Test input (float32):', Array.from(testInput));

        // Convert to 16-bit PCM
        const pcmData = new Int16Array(testInput.length);
        for (let i = 0; i < testInput.length; i++) {
            pcmData[i] = Math.max(-32768, Math.min(32767, testInput[i] * 32768));
        }
        console.log('PCM data (int16):', Array.from(pcmData));

        // Convert to bytes
        const byteArray = new Uint8Array(pcmData.buffer);
        console.log('Byte array (uint8):', Array.from(byteArray));
        console.log('Byte array length:', byteArray.length);
        console.log('Expected: 10 bytes (5 samples * 2 bytes each)');

        // Test what happens when we send this data
        if (window.sendAudioData) {
            const testBytes = Array.from(byteArray);
            console.log('Sending test data to sendAudioData...');
            window.sendAudioData(testBytes);
        }

        return {
            testInput: Array.from(testInput),
            pcmData: Array.from(pcmData),
            byteArray: Array.from(byteArray),
            byteLength: byteArray.length
        };
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("Audio conversion debug failed: %v", err)
        return err
    }

    log.Printf("Audio Conversion Debug: %v", result)
    return nil
}


func (bac *BrowserAudioCapturer) extractVoiceBridgeInfo() error {
    log.Printf("Extracting voice bridge information...")

    jsCode := `() => {
        console.log('[BBB-Bot] Extracting voice bridge info');

        function findVoiceBridge() {
            // Method 1: Look for voice bridge in UI
            const bridgeSelectors = [
                '[data-test="voiceBridge"]',
                '[aria-label*="bridge" i]',
                '[title*="bridge" i]',
                '.voice-bridge',
                '.conference-bridge',
                '.phone-number',
                '*:contains("bridge" i)',
                '*:contains("phone" i)',
                '*:contains("dial" i)'
            ];

            let bridgeInfo = {
                elements: [],
                numbers: [],
                rawText: []
            };

            bridgeSelectors.forEach(selector => {
                try {
                    const elements = document.querySelectorAll(selector);
                    elements.forEach(el => {
                        const text = el.textContent || el.innerText || '';
                        if (text && text.length > 0) {
                            bridgeInfo.elements.push({
                                selector: selector,
                                text: text.trim().substring(0, 100)
                            });

                            // Extract numbers (potential bridge numbers)
                            const numbers = text.match(/\d+/g);
                            if (numbers) {
                                numbers.forEach(num => {
                                    if (num.length >= 3) { // Likely a bridge number
                                        bridgeInfo.numbers.push(num);
                                    }
                                });
                            }
                        }
                    });
                } catch (e) {}
            });

            // Method 2: Check conference info
            if (window.APP && window.APP.conference && window.APP.conference.room) {
                const room = window.APP.conference.room;
                bridgeInfo.conferenceRoom = {
                    voiceBridge: room.voiceBridge,
                    meetingId: room.meetingId
                };
            }

            // Method 3: Check URL parameters
            const urlParams = new URLSearchParams(window.location.search);
            bridgeInfo.urlParams = {};
            urlParams.forEach((value, key) => {
                if (key.includes('bridge') || key.includes('voice') || key.includes('phone')) {
                    bridgeInfo.urlParams[key] = value;
                }
            });

            console.log('Voice bridge extraction:', bridgeInfo);
            return bridgeInfo;
        }

        return findVoiceBridge();
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("Voice bridge extraction failed: %v", err)
        return err
    }

    log.Printf("Voice Bridge Info: %v", result)
    return nil
}

func (bac *BrowserAudioCapturer) captureBBBAudioBridge() error {
    log.Printf("Attempting to capture via BBB audio bridge...")

    jsCode := `() => {
        console.log('[BBB-Bot] Attempting BBB audio bridge capture');

        // Strategy 1: Hook into BBB's internal audio system
        function hookIntoBBBAudioSystem() {
            console.log('Searching for BBB audio bridge components...');

            // Look for common BBB audio objects
            const audioTargets = [
                'audioBridge',
                'audioConsumer',
                'audioProvider',
                'audioManager',
                'mediasoup',
                'rtcPeer',
                'webRTCPeer'
            ];

            let foundComponents = {};

            audioTargets.forEach(target => {
                // Search in global scope
                if (window[target]) {
                    foundComponents[target] = { exists: true, type: typeof window[target] };
                    console.log('Found audio component:', target);
                }

                // Search in App/APP scope
                if (window.App && window.App[target]) {
                    foundComponents['App.' + target] = { exists: true, type: typeof window.App[target] };
                    console.log('Found audio component: App.' + target);
                }

                if (window.APP && window.APP[target]) {
                    foundComponents['APP.' + target] = { exists: true, type: typeof window.APP[target] };
                    console.log('Found audio component: APP.' + target);
                }
            });

            return foundComponents;
        }

        // Strategy 2: Monitor for audio track events
        function monitorAudioTracks() {
            console.log('Setting up audio track monitoring...');

            // Override addTrack to catch new audio tracks
            if (window.MediaStream && window.MediaStream.prototype) {
                const originalAddTrack = window.MediaStream.prototype.addTrack;
                window.MediaStream.prototype.addTrack = function(track) {
                    console.log('MediaStream.addTrack called:', track.kind, track.id);
                    if (track.kind === 'audio') {
                        console.log('🎵 AUDIO TRACK ADDED:', track);
                        processAudioTrack(track);
                    }
                    return originalAddTrack.call(this, track);
                };
            }

            // Monitor RTCPeerConnection for audio tracks
            if (window.RTCPeerConnection && window.RTCPeerConnection.prototype) {
                const originalAddTrack = window.RTCPeerConnection.prototype.addTrack;
                window.RTCPeerConnection.prototype.addTrack = function(track, stream) {
                    console.log('RTCPeerConnection.addTrack:', track.kind, track.id);
                    if (track.kind === 'audio') {
                        console.log('🎵 PEER CONNECTION AUDIO TRACK:', track);
                        processAudioTrack(track);
                    }
                    return originalAddTrack.call(this, track, ...Array.from(arguments).slice(1));
                };
            }
        }

        function processAudioTrack(track) {
            console.log('Processing audio track:', track.id, track.readyState);

            if (track.readyState !== 'live') {
                console.log('Track not live, waiting...');
                track.onended = () => console.log('Audio track ended');
                return;
            }

            try {
                const stream = new MediaStream([track]);
                const audioContext = new (window.AudioContext || window.webkitAudioContext)({
                    sampleRate: 16000
                });

                const source = audioContext.createMediaStreamSource(stream);
                const processor = audioContext.createScriptProcessor(2048, 1, 1);

                source.connect(processor);
                processor.connect(audioContext.destination);

                let packetCount = 0;

                processor.onaudioprocess = function(event) {
                    const inputData = event.inputBuffer.getChannelData(0);

                    // Check for non-silent audio
                    let hasAudio = false;
                    let maxAmplitude = 0;
                    for (let i = 0; i < inputData.length; i++) {
                        const amplitude = Math.abs(inputData[i]);
                        if (amplitude > 0.0001) {
                            hasAudio = true;
                            maxAmplitude = Math.max(maxAmplitude, amplitude);
                        }
                    }

                    if (!hasAudio) return;

                    const pcmData = new Int16Array(inputData.length);
                    for (let i = 0; i < inputData.length; i++) {
                        pcmData[i] = Math.max(-32768, Math.min(32767, inputData[i] * 32768));
                    }

                    const byteArray = new Uint8Array(pcmData.buffer);

                    if (window.sendAudioData) {
                        window.sendAudioData(Array.from(byteArray));
                        packetCount++;

                        if (packetCount % 50 === 0) {
                            console.log('Audio bridge packet:', packetCount, 'amp:', maxAmplitude.toFixed(4));
                        }
                    }
                };

                console.log('✅ Successfully hooked into audio track via bridge');

            } catch (error) {
                console.error('Error processing audio track:', error);
            }
        }

        // Strategy 3: Look for existing audio tracks
        function findExistingTracks() {
            console.log('Searching for existing audio tracks...');

            // Check all MediaStreams
            for (let key in window) {
                try {
                    const obj = window[key];
                    if (obj instanceof MediaStream) {
                        const audioTracks = obj.getAudioTracks();
                        if (audioTracks.length > 0) {
                            console.log('Found MediaStream with audio tracks:', key, audioTracks.length);
                            audioTracks.forEach(track => processAudioTrack(track));
                        }
                    }
                } catch (e) {}
            }

            // Check HTML5 audio elements
            const audioElements = document.querySelectorAll('audio');
            audioElements.forEach((audio, idx) => {
                if (audio.srcObject) {
                    console.log('Audio element with srcObject:', idx);
                    const tracks = audio.srcObject.getAudioTracks();
                    tracks.forEach(track => processAudioTrack(track));
                }
            });
        }

        // Execute strategies
        const components = hookIntoBBBAudioSystem();
        monitorAudioTracks();
        findExistingTracks();

        return {
            strategy: 'audio_bridge_capture',
            foundComponents: components,
            status: 'monitoring_started'
        };
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("BBB audio bridge capture failed: %v", err)
        return err
    }

    log.Printf("BBB Audio Bridge Capture: %v", result)
    return nil
}

func (bac *BrowserAudioCapturer) debugBBBEnvironment() {
        log.Printf("Debugging BBB environment in detail...")

        jsCode := `() => {
                console.log('=== BBB ENVIRONMENT DEBUG ===');

                const debugInfo = {};

                // Check for BBB objects with better error handling
                debugInfo.APP = !!window.APP;
                debugInfo.App = !!window.App;
                debugInfo.BBB = !!window.BBB;

                // Check conference with safe navigation
                debugInfo.conference = !!(window.APP && window.APP.conference) || !!(window.App && window.App.conference);
                debugInfo.room = false;

                if (window.APP && window.APP.conference && window.APP.conference._room) {
                        debugInfo.room = true;
                        const room = window.APP.conference._room;
                        debugInfo.audioConsumers = room._audioConsumers ? room._audioConsumers.size : 0;
                } else if (window.App && window.App.conference && window.App.conference._room) {
                        debugInfo.room = true;
                        const room = window.App.conference._room;
                        debugInfo.audioConsumers = room._audioConsumers ? room._audioConsumers.size : 0;
                }

                // Check if we're in a conference
                debugInfo.currentPage = window.location.href;
                debugInfo.inConference = debugInfo.currentPage.includes('html5client');

                // Check for audio context support
                debugInfo.audioContext = !!(window.AudioContext || window.webkitAudioContext);

                console.log('BBB Environment:', debugInfo);
                return debugInfo;
        }`

        result, err := bac.page.Eval(jsCode)
        if err != nil {
                log.Printf("BBB environment debug failed: %v", err)
        } else {
                log.Printf("BBB Environment: %v", result)
        }
}



func (bac *BrowserAudioCapturer) verifySendAudioFunction() error {
    log.Printf("Verifying sendAudioData function...")

    funcName, err := bac.page.Expose("sendAudioData", func(data gson.JSON) (interface{}, error) {
        if data.Nil() {
            log.Printf("❌ sendAudioData received nil data")
            return nil, nil
        }

        dataArr := data.Arr()
        if len(dataArr) == 0 {
            log.Printf("❌ sendAudioData received empty array")
            return nil, nil
        }

        log.Printf("🚨 sendAudioData processing %d bytes from JavaScript", len(dataArr))

        // 🆕 ADD PCM RANGE ANALYSIS
        if len(dataArr) >= 4 {
            // Try to interpret as 16-bit PCM samples (2 bytes per sample)
            var pcmSamples []int16
            for i := 0; i < len(dataArr)-1; i += 2 {
                // Combine two bytes into a 16-bit signed integer
                lowByte := byte(dataArr[i].Num())
                highByte := byte(dataArr[i+1].Num())
                sample := int16(highByte)<<8 | int16(lowByte)
                pcmSamples = append(pcmSamples, sample)
            }

            if len(pcmSamples) > 0 {
                // Analyze PCM range
                minPCM := int(32768)
                maxPCM := int(-32768)
                for _, sample := range pcmSamples {
                    s := int(sample)
                    if s < minPCM { minPCM = s }
                    if s > maxPCM { maxPCM = s }
                }
                log.Printf("🔧 PCM RANGE: %d to %d (should be -32768 to 32767)", minPCM, maxPCM)

                // Check if this looks like real PCM data
                if minPCM >= 0 && maxPCM <= 255 {
                    log.Printf("❌ DATA IS 8-BIT, NOT 16-BIT PCM!")
                } else if math.Abs(float64(minPCM)) > 100 || math.Abs(float64(maxPCM)) > 100 {
                    log.Printf("✅ Data looks like real 16-bit PCM")
                }
            }
        }

        // Existing analysis
        zeroCount := 0
        nonZeroCount := 0
        maxValue := 0
        minValue := 0

        for i := 0; i < len(dataArr) && i < 100; i++ {
            val := int(dataArr[i].Num())
            if val == 0 {
                zeroCount++
            } else {
                nonZeroCount++
                if val > maxValue { maxValue = val }
                if val < minValue { minValue = val }
            }
        }

        zeroPercentage := float64(zeroCount) / float64(len(dataArr)) * 100
        log.Printf("📊 Byte Analysis: %d zeros (%.1f%%), %d non-zero, range [%d, %d]",
            zeroCount, zeroPercentage, nonZeroCount, minValue, maxValue)

        byteData := make([]byte, len(dataArr))
        for i, v := range dataArr {
            num := v.Num()
            byteData[i] = byte(num)
        }

        if bac.audioConn != nil && len(byteData) > 0 {
            n, err := bac.audioConn.Write(byteData)
            if err != nil {
                log.Printf("❌ UDP send failed: %v", err)
                return nil, err
            }
            log.Printf("✅ UDP send successful: %d bytes sent to transcription-service", n)
        } else {
            log.Printf("❌ UDP connection not available or empty data")
        }
        return nil, nil
    })

    if err != nil {
        log.Printf("Failed to expose sendAudioData function: %v", err)
        return err
    }
    log.Printf("Exposed function: %s", funcName)
    return nil
}



func (bac *BrowserAudioCapturer) monitorAudioCapture() error {
    log.Printf("Starting comprehensive audio capture monitoring...")

    jsCode := `() => {
        console.log('[BBB-Bot] Starting comprehensive audio monitoring');

        let totalBytesSent = 0;
        let packetCount = 0;
        let audioContexts = [];

        // Monitor all sendAudioData calls with detailed analysis
        const originalSendAudioData = window.sendAudioData;
        window.sendAudioData = function(data) {
            if (!data || !Array.isArray(data)) {
                console.error('Invalid data sent to sendAudioData');
                return;
            }

            totalBytesSent += data.length;
            packetCount++;

            // Detailed audio analysis
            let zeroCount = 0;
            let nonZeroCount = 0;
            let maxValue = 0;
            let minValue = 255;
            let sum = 0;

            for (let i = 0; i < Math.min(data.length, 200); i++) {
                const val = data[i];
                if (val === 0) {
                    zeroCount++;
                } else {
                    nonZeroCount++;
                    maxValue = Math.max(maxValue, val);
                    minValue = Math.min(minValue, val);
                    sum += val;
                }
            }

            const zeroPercentage = (zeroCount / data.length * 100).toFixed(1);
            const avgValue = nonZeroCount > 0 ? (sum / nonZeroCount).toFixed(1) : 0;
            const dynamicRange = maxValue - minValue;

            // Log audio quality assessment
            if (packetCount % 5 === 0) {
                let quality = "SILENCE";
                if (nonZeroCount > 0) {
                    if (dynamicRange > 100) quality = "GOOD AUDIO";
                    else if (dynamicRange > 50) quality = "WEAK AUDIO";
                    else quality = "NOISE";
                }

                console.log('🎵 Packet #' + packetCount + ': ' + data.length + ' bytes | ' +
                          'Zeros: ' + zeroPercentage + '% | Range: ' + dynamicRange +
                          ' | Quality: ' + quality);
            }

            if (packetCount % 50 === 0) {
                console.log('📈 TOTAL: ' + packetCount + ' packets, ' + totalBytesSent + ' bytes sent');
            }

            // Call original function
            if (originalSendAudioData) {
                return originalSendAudioData(data);
            }
        };

        // Monitor AudioContext creation and processing
        const OriginalAudioContext = window.AudioContext || window.webkitAudioContext;
        if (OriginalAudioContext) {
            window.AudioContext = function(...args) {
                const context = new OriginalAudioContext(...args);
                console.log('🎵 AudioContext created - SampleRate: ' + context.sampleRate + ', State: ' + context.state);
                audioContexts.push(context);

                // Monitor when audio actually starts processing
                context.onstatechange = () => {
                    console.log('🔊 AudioContext state changed: ' + context.state);
                };

                return context;
            };
            window.AudioContext.prototype = OriginalAudioContext.prototype;
        }

        // Enhanced audio element monitoring
        function checkAudioElements() {
            const audios = document.querySelectorAll('audio');
            console.log('🔊 Found ' + audios.length + ' audio elements:');

            let activeAudios = 0;
            audios.forEach((audio, idx) => {
                const isActive = !audio.paused && !audio.muted && audio.readyState > 0;
                if (isActive) activeAudios++;

                console.log('  Audio[' + idx + ']: ' +
                    'paused=' + audio.paused +
                    ', muted=' + audio.muted +
                    ', readyState=' + audio.readyState +
                    ', active=' + isActive);
            });

            console.log('🎯 Active audio elements: ' + activeAudios + '/' + audios.length);
        }

        // Monitor for MediaStreams (WebRTC audio)
        function monitorMediaStreams() {
            for (let key in window) {
                try {
                    if (window[key] instanceof MediaStream) {
                        const stream = window[key];
                        const audioTracks = stream.getAudioTracks();
                        if (audioTracks.length > 0 && !stream.monitored) {
                            console.log('🎤 Found MediaStream with ' + audioTracks.length + ' audio tracks: ' + key);
                            stream.monitored = true;

                            audioTracks.forEach((track, idx) => {
                                console.log('  Track[' + idx + ']: ' +
                                    'enabled=' + track.enabled +
                                    'muted=' + track.muted +
                                    ', readyState=' + track.readyState);
                            });
                        }
                    }
                } catch (e) {}
            }
        }

        // Periodic monitoring
        setInterval(() => {
            checkAudioElements();
            monitorMediaStreams();
        }, 5000);

        // Initial check
        checkAudioElements();
        monitorMediaStreams();

        return {
            monitoring: 'active',
            audioElements: document.querySelectorAll('audio').length
        };
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("Audio monitoring failed: %v", err)
        return err
    }

    log.Printf("Audio Monitoring: %v", result)
    return nil
}


func (bac *BrowserAudioCapturer) captureBBBHTML5Audio() error {
    log.Printf("Integrating with BBB HTML5 audio system...")

    jsCode := `() => {
        console.log('[BBB-Bot] Integrating with BBB HTML5 Audio System');

        // Main BBB audio integration class
        class BBBAudioIntegration {
            constructor() {
                this.audioContext = null;
                this.processors = new Map();
                this.isConnected = false;
                this.setupBBBAudioCapture();
            }

            setupBBBAudioCapture() {
                console.log('Setting up BBB audio capture...');

                // Wait for BBB to initialize
                setTimeout(() => {
                    this.hookIntoBBBAudioSystem();
                }, 5000);
            }

            hookIntoBBBAudioSystem() {
                try {
                    // Strategy 1: Hook into BBB's App.audio system
                    if (window.App && window.App.audio) {
                        console.log('Found BBB App.audio system, hooking in...');
                        this.hookIntoAppAudio(window.App.audio);
                        return;
                    }

                    // Strategy 2: Hook into APP.conference for newer BBB versions
                    if (window.APP && window.APP.conference) {
                        console.log('Found BBB APP.conference system, hooking in...');
                        this.hookIntoAPPConference(window.APP.conference);
                        return;
                    }

                    // Strategy 3: Direct audio element capture (fallback)
                    console.log('BBB audio system not found, using direct capture...');
                    this.captureAudioElementsDirectly();

                } catch (error) {
                    console.error('Error hooking into BBB audio:', error);
                }
            }

            hookIntoAppAudio(audioSystem) {
                console.log('Hooking into App.audio system...');

                // Override the method that handles incoming audio streams
                if (audioSystem.addStream) {
                    const originalAddStream = audioSystem.addStream;
                    audioSystem.addStream = (stream, userId) => {
                        console.log('🎵 App.audio.addStream called for user:', userId);

                        // Process the audio stream
                        this.processAudioStream(stream, 'App.audio-' + userId);

                        // Call original method
                        return originalAddStream.call(audioSystem, stream, userId);
                    };
                    console.log('✅ Successfully hooked into App.audio.addStream');
                }

                // Also process any existing streams
                if (audioSystem.consumers) {
                    console.log('Processing existing audio consumers...');
                    Object.values(audioSystem.consumers).forEach(consumer => {
                        if (consumer.stream) {
                            this.processAudioStream(consumer.stream, 'existing-consumer');
                        }
                    });
                }
            }

            hookIntoAPPConference(conference) {
                console.log('Hooking into APP.conference system...');

                // Hook into conference audio events
                if (conference._room && conference._room._audioConsumers) {
                    const audioConsumers = conference._room._audioConsumers;
                    console.log('Found audio consumers:', audioConsumers.size);

                    // Monitor for new audio consumers
                    const originalSet = audioConsumers.set;
                    audioConsumers.set = (key, consumer) => {
                        console.log('🎵 New audio consumer added:', key);

                        if (consumer && consumer._track) {
                            setTimeout(() => {
                                const stream = new MediaStream([consumer._track]);
                                this.processAudioStream(stream, 'consumer-' + key);
                            }, 1000);
                        }

                        return originalSet.call(audioConsumers, key, consumer);
                    };

                    // Process existing consumers
                    audioConsumers.forEach((consumer, key) => {
                        if (consumer && consumer._track) {
                            const stream = new MediaStream([consumer._track]);
                            this.processAudioStream(stream, 'existing-' + key);
                        }
                    });
                }

                // Hook into audio service if available
                if (conference.audioService) {
                    console.log('Found conference.audioService');
                    this.hookIntoAudioService(conference.audioService);
                }
            }

            hookIntoAudioService(audioService) {
                // Hook into audio service events
                if (audioService.addRemoteStream) {
                    const originalAddRemoteStream = audioService.addRemoteStream;
                    audioService.addRemoteStream = (stream, userId) => {
                        console.log('🎵 AudioService.addRemoteStream:', userId);
                        this.processAudioStream(stream, 'audioService-' + userId);
                        return originalAddRemoteStream.call(audioService, stream, userId);
                    };
                }
            }

            captureAudioElementsDirectly() {
                console.log('Capturing audio elements directly...');

                // Monitor for audio element creation
                const observer = new MutationObserver((mutations) => {
                    mutations.forEach((mutation) => {
                        mutation.addedNodes.forEach((node) => {
                            if (node.nodeName === 'AUDIO') {
                                console.log('New audio element detected');
                                this.hookIntoAudioElement(node, 'mutation-observer');
                            }
                        });
                    });
                });

                observer.observe(document.body, {
                    childList: true,
                    subtree: true
                });

                // Process existing audio elements
                const existingAudioElements = document.querySelectorAll('audio');
                console.log('Found existing audio elements:', existingAudioElements.length);

                existingAudioElements.forEach((audio, index) => {
                    this.hookIntoAudioElement(audio, 'existing-' + index);
                });
            }

            hookIntoAudioElement(audioElement, source) {
                if (audioElement.processed) return;
                audioElement.processed = true;

                console.log('Hooking into audio element:', source);

                // Wait for the audio element to have a stream
                const checkStream = () => {
                    if (audioElement.srcObject) {
                        console.log('Audio element has srcObject:', audioElement.srcObject.id);
                        this.processAudioStream(audioElement.srcObject, 'audio-element-' + source);
                        return;
                    }

                    // Check again
                    setTimeout(checkStream, 1000);
                };

                checkStream();
            }

            processAudioStream(stream, source) {
                if (stream.processed) return;
                stream.processed = true;

                console.log('🎵 Processing audio stream from:', source, stream.id);

                try {
                    if (!this.audioContext) {
                        this.audioContext = new (window.AudioContext || window.webkitAudioContext)({
                            sampleRate: 16000
                        });
                    }

                    const sourceNode = this.audioContext.createMediaStreamSource(stream);
                    const processor = this.audioContext.createScriptProcessor(2048, 1, 1);

                    sourceNode.connect(processor);
                    processor.connect(this.audioContext.destination);

                    let packetCount = 0;
                    let lastLogTime = Date.now();

                    processor.onaudioprocess = (event) => {
                        const inputData = event.inputBuffer.getChannelData(0);

                        // 🆕 FIXED: Check for actual audio with LOWER threshold
                        let hasAudio = false;
                        let maxAmplitude = 0;
                        let rms = 0;

                        for (let i = 0; i < inputData.length; i++) {
                            const amplitude = Math.abs(inputData[i]);
                            rms += amplitude * amplitude;
                            if (amplitude > 0.0001) {  // 🆕 LOWERED from 0.001 to 0.0001
                                hasAudio = true;
                                maxAmplitude = Math.max(maxAmplitude, amplitude);
                            }
                        }

                        rms = Math.sqrt(rms / inputData.length);

                        // 🆕 FIXED: Only skip if completely silent, don't use high threshold
                        if (rms < 0.00005) {  // 🆕 Only skip if virtually silent
                            return;
                        }

                        // Convert to PCM
                        const pcmData = new Int16Array(inputData.length);
                        for (let i = 0; i < inputData.length; i++) {
                            pcmData[i] = Math.max(-32768, Math.min(32767, inputData[i] * 32768));
                        }

                        const byteArray = new Uint8Array(pcmData.buffer);

                        if (window.sendAudioData) {
                            window.sendAudioData(Array.from(byteArray));
                            packetCount++;

                            if (packetCount % 25 === 0 || Date.now() - lastLogTime > 5000) {
                                console.log('🎵 Audio packet', packetCount, 'from', source,
                                          'samples:', inputData.length,
                                          'rms:', rms.toFixed(6),  // 🆕 More precision
                                          'max:', maxAmplitude.toFixed(6));
                                lastLogTime = Date.now();
                            }
                        }
                    };

                    this.processors.set(source, processor);
                    console.log('✅ Successfully processing audio from:', source);

                } catch (error) {
                    console.error('Error processing audio stream:', error);
                }
            }
        }

        // Initialize BBB audio integration
        window.bbbAudioIntegration = new BBBAudioIntegration();

        return {
            status: 'bbb_audio_integration_started',
            method: 'html5_client_integration'
        };
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("BBB HTML5 audio integration failed: %v", err)
        return err
    }

    log.Printf("BBB HTML5 Audio Integration: %v", result)
    return nil
}


func (bac *BrowserAudioCapturer) detectBBBEnvironment() error {
    log.Printf("Detecting BBB environment...")

    jsCode := `() => {
        console.log('=== BBB ENVIRONMENT DETECTION ===');

        const detection = {
            // Core BBB objects
            hasAPP: !!window.APP,
            hasApp: !!window.App,
            hasBBB: !!window.BBB,

            // Conference objects
            hasConference: !!(window.APP && window.APP.conference) || !!(window.App && window.App.conference),
            hasRoom: false,
            hasAudioConsumers: false,

            // Audio systems
            hasAudioManager: !!(window.APP && window.APP.audioManager) || !!(window.App && window.App.audioManager),
            hasAudioService: !!(window.APP && window.APP.audioService) || !!(window.App && window.App.audioService),

            // Media systems
            hasMediaStreams: false,
            audioElements: document.querySelectorAll('audio').length,

            // Version detection
            version: 'unknown'
        };

        // Check room and consumers
        if (window.APP && window.APP.conference && window.APP.conference._room) {
            detection.hasRoom = true;
            detection.hasAudioConsumers = !!window.APP.conference._room._audioConsumers;
            detection.audioConsumersCount = window.APP.conference._room._audioConsumers ? window.APP.conference._room._audioConsumers.size : 0;
        } else if (window.App && window.App.conference && window.App.conference._room) {
            detection.hasRoom = true;
            detection.hasAudioConsumers = !!window.App.conference._room._audioConsumers;
            detection.audioConsumersCount = window.App.conference._room._audioConsumers ? window.App.conference._room._audioConsumers.size : 0;
        }

        // Check for media streams
        for (let key in window) {
            try {
                if (window[key] instanceof MediaStream) {
                    detection.hasMediaStreams = true;
                    break;
                }
            } catch (e) {}
        }

        // Try to detect BBB version
        if (window.APP && window.APP.VERSION) {
            detection.version = window.APP.VERSION;
        } else if (window.BBB && window.BBB.version) {
            detection.version = window.BBB.version;
        }

        console.log('BBB Environment Detection:', detection);
        return detection;
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("BBB environment detection failed: %v", err)
        return err
    }

    log.Printf("BBB Environment Detection: %v", result)
    return nil
}

func (bac *BrowserAudioCapturer) captureBBBMediasoupAudio() error {
    log.Printf("Capturing BBB Mediasoup audio streams...")

    jsCode := `() => {
        console.log('[BBB-Bot] Targeting BBB Mediasoup audio system');

        class BBBMediasoupCapture {
            constructor() {
                this.audioContext = null;
                this.processors = new Map();
                this.setupMediasoupCapture();
            }

            setupMediasoupCapture() {
                console.log('Setting up Mediasoup audio capture...');

                // Strategy 1: Hook into APP.conference audio consumers
                this.hookIntoConferenceAudio();

                // Strategy 2: Monitor for new audio consumers
                this.monitorAudioConsumers();

                // Strategy 3: Hook into Mediasoup directly
                this.hookIntoMediasoup();
            }

            hookIntoConferenceAudio() {
                if (!window.APP || !window.APP.conference) {
                    console.log('APP.conference not available yet, retrying...');
                    setTimeout(() => this.hookIntoConferenceAudio(), 2000);
                    return;
                }

                console.log('Found APP.conference, hooking into audio system...');

                const conference = window.APP.conference;

                // Hook into the room's audio consumers
                if (conference._room && conference._room._audioConsumers) {
                    console.log('Found audio consumers:', conference._room._audioConsumers.size);

                    // Process existing consumers
                    conference._room._audioConsumers.forEach((consumer, id) => {
                        this.processAudioConsumer(consumer, id);
                    });

                    // Monitor for new consumers
                    const originalSet = conference._room._audioConsumers.set;
                    conference._room._audioConsumers.set = (key, consumer) => {
                        console.log('🎵 New audio consumer added:', key);
                        this.processAudioConsumer(consumer, key);
                        return originalSet.call(conference._room._audioConsumers, key, consumer);
                    };
                }

                // Hook into user join events
                if (conference._room && conference._room.on) {
                    conference._room.on('userjoined', (user) => {
                        console.log('User joined:', user.id);
                        // Audio consumers might be added shortly after
                        setTimeout(() => {
                            if (conference._room._audioConsumers) {
                                conference._room._audioConsumers.forEach((consumer, id) => {
                                    if (id.includes(user.id) && !consumer.processed) {
                                        this.processAudioConsumer(consumer, id);
                                    }
                                });
                            }
                        }, 3000);
                    });
                }
            }

            processAudioConsumer(consumer, consumerId) {
                if (consumer.processed || !consumer._track) {
                    return;
                }
                consumer.processed = true;

                console.log('🎵 Processing audio consumer:', consumerId, consumer._track.id);

                try {
                    const stream = new MediaStream([consumer._track]);
                    this.processAudioStream(stream, 'consumer-' + consumerId);

                    // Monitor track for changes
                    consumer._track.onended = () => {
                        console.log('Audio track ended:', consumerId);
                    };

                    consumer._track.onmute = () => {
                        console.log('Audio track muted:', consumerId);
                    };

                    consumer._track.onunmute = () => {
                        console.log('Audio track unmuted:', consumerId);
                    };

                } catch (error) {
                    console.error('Error processing audio consumer:', error);
                }
            }

            hookIntoMediasoup() {
                // Hook into Mediasoup device creation
                if (window.mediasoupClient) {
                    console.log('Found mediasoupClient, hooking in...');
                }

                // Hook into transport consumers
                this.hookIntoTransportConsumers();
            }

            hookIntoTransportConsumers() {
                // Monitor for transport consumer events
                if (window.APP && window.APP.conference && window.APP.conference._room) {
                    const room = window.APP.conference._room;

                    if (room._transport && room._transport.consumer) {
                        const originalConsumer = room._transport.consumer;
                        room._transport.consumer = async (options) => {
                            console.log('Transport consumer created:', options);
                            const consumer = await originalConsumer.call(room._transport, options);

                            // Process audio consumers
                            if (consumer && consumer.kind === 'audio') {
                                setTimeout(() => {
                                    if (consumer._track) {
                                        const stream = new MediaStream([consumer._track]);
                                        this.processAudioStream(stream, 'transport-' + consumer.id);
                                    }
                                }, 1000);
                            }

                            return consumer;
                        };
                    }
                }
            }

            monitorAudioConsumers() {
                // Periodic check for new audio consumers
                setInterval(() => {
                    if (window.APP && window.APP.conference && window.APP.conference._room) {
                        const room = window.APP.conference._room;
                        if (room._audioConsumers) {
                            room._audioConsumers.forEach((consumer, id) => {
                                if (!consumer.processed && consumer._track) {
                                    console.log('Found unprocessed audio consumer:', id);
                                    this.processAudioConsumer(consumer, id);
                                }
                            });
                        }
                    }
                }, 5000);
            }

            processAudioStream(stream, source) {
                if (stream.processed) return;
                stream.processed = true;

                console.log('🎵 Processing audio stream from:', source, stream.id);

                try {
                    if (!this.audioContext) {
                        this.audioContext = new (window.AudioContext || window.webkitAudioContext)({
                            sampleRate: 16000
                        });
                    }

                    const sourceNode = this.audioContext.createMediaStreamSource(stream);
                    const processor = this.audioContext.createScriptProcessor(2048, 1, 1);

                    sourceNode.connect(processor);
                    processor.connect(this.audioContext.destination);

                    let packetCount = 0;
                    let lastLogTime = Date.now();
                    let silentPackets = 0;

                    processor.onaudioprocess = (event) => {
                        const inputData = event.inputBuffer.getChannelData(0);

                        // Calculate RMS for silence detection
                        let rms = 0;
                        let maxAmplitude = 0;
                        for (let i = 0; i < inputData.length; i++) {
                            const amplitude = Math.abs(inputData[i]);
                            rms += amplitude * amplitude;
                            if (amplitude > maxAmplitude) {
                                maxAmplitude = amplitude;
                            }
                        }
                        rms = Math.sqrt(rms / inputData.length);

                        // Skip if completely silent (adjust threshold as needed)
                        if (rms < 0.0001) {
                            silentPackets++;
                            if (silentPackets % 100 === 0) {
                                console.log('Silent audio packets:', silentPackets, 'from', source);
                            }
                            return;
                        }
                        silentPackets = 0;

                        // Convert to 16-bit PCM
                        const pcmData = new Int16Array(inputData.length);
                        for (let i = 0; i < inputData.length; i++) {
                            pcmData[i] = Math.max(-32768, Math.min(32767, inputData[i] * 32768));
                        }

                        const byteArray = new Uint8Array(pcmData.buffer);

                        if (window.sendAudioData) {
                            window.sendAudioData(Array.from(byteArray));
                            packetCount++;

                            if (packetCount % 50 === 0 || Date.now() - lastLogTime > 10000) {
                                console.log('🎵 Mediasoup audio packet', packetCount, 'from', source,
                                          'samples:', inputData.length,
                                          'rms:', rms.toFixed(6),
                                          'max:', maxAmplitude.toFixed(6));
                                lastLogTime = Date.now();
                            }
                        }
                    };

                    this.processors.set(source, processor);
                    console.log('✅ Successfully processing Mediasoup audio from:', source);

                } catch (error) {
                    console.error('Error processing Mediasoup audio stream:', error);
                }
            }
        }

        // Initialize Mediasoup capture
        window.bbbMediasoupCapture = new BBBMediasoupCapture();

        return {
            status: 'mediasoup_capture_started',
            method: 'conference_audio_consumers'
        };
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("BBB Mediasoup audio capture failed: %v", err)
        return err
    }

    log.Printf("BBB Mediasoup Audio Capture: %v", result)
    return nil
}

func (bac *BrowserAudioCapturer) captureBBBUniversalAudio() error {
    log.Printf("Using universal BBB audio capture approach...")

    jsCode := `() => {
        console.log('[BBB-Bot] Starting universal BBB audio capture');

        class UniversalBBBAudioCapture {
            constructor() {
                this.audioContext = null;
                this.processedStreams = new Set();
                this.processedElements = new Set();
                this.setupUniversalCapture();
            }

            setupUniversalCapture() {
                console.log('Setting up universal audio capture...');

                // Strategy 1: Monitor ALL media streams on the page
                this.monitorAllMediaStreams();

                // Strategy 2: Hook into ALL audio elements
                this.hookIntoAllAudioElements();

                // Strategy 3: Override WebRTC methods globally
                this.hookIntoWebRTCGlobally();

                // Strategy 4: Check for BBB in shadow DOM and frames
                this.searchInAllContexts();

                // Strategy 5: Periodic discovery
                this.startPeriodicDiscovery();
            }

            searchInAllContexts() {
                console.log('Searching for audio in all contexts...');

                // Check frames/iframes
                try {
                    for (let i = 0; i < window.frames.length; i++) {
                        try {
                            const frame = window.frames[i];
                            if (frame && frame.document) {
                                console.log('Checking frame:', i);
                                this.checkFrameForAudio(frame, 'frame-' + i);
                            }
                        } catch (e) {
                            console.log('Cannot access frame:', i, e.toString());
                        }
                    }
                } catch (e) {
                    console.log('Cannot access frames:', e.toString());
                }

                // Check shadow DOM
                this.searchShadowDOM(document.body);
            }

            checkFrameForAudio(frame, frameId) {
                try {
                    // Check for audio elements in frame
                    const frameAudios = frame.document.querySelectorAll('audio');
                    console.log('Frame', frameId, 'has', frameAudios.length, 'audio elements');

                    frameAudios.forEach((audio, idx) => {
                        this.hookIntoAudioElement(audio, frameId + '-audio-' + idx);
                    });

                    // Check for MediaStreams in frame
                    for (let key in frame) {
                        try {
                            const obj = frame[key];
                            if (obj instanceof MediaStream) {
                                const audioTracks = obj.getAudioTracks();
                                if (audioTracks.length > 0 && !this.processedStreams.has(obj.id)) {
                                    console.log('🎵 Found MediaStream in frame:', frameId, key, obj.id);
                                    this.processMediaStream(obj, 'frame-' + frameId + '-' + key);
                                }
                            }
                        } catch (e) {
                            // Skip inaccessible properties
                        }
                    }

                } catch (e) {
                    console.log('Cannot access frame content:', frameId, e.toString());
                }
            }

            searchShadowDOM(element) {
                try {
                    // Check if element has shadow root
                    if (element.shadowRoot) {
                        console.log('Found shadow root');
                        const shadowAudios = element.shadowRoot.querySelectorAll('audio');
                        shadowAudios.forEach((audio, idx) => {
                            this.hookIntoAudioElement(audio, 'shadow-audio-' + idx);
                        });

                        // Recursively search in shadow DOM
                        element.shadowRoot.querySelectorAll('*').forEach(child => {
                            this.searchShadowDOM(child);
                        });
                    }

                    // Check children
                    element.querySelectorAll('*').forEach(child => {
                        if (child.shadowRoot) {
                            this.searchShadowDOM(child);
                        }
                    });
                } catch (e) {
                    console.log('Error searching shadow DOM:', e.toString());
                }
            }

            monitorAllMediaStreams() {
                console.log('Monitoring ALL media streams on page...');

                // Override MediaStream constructor
                const OriginalMediaStream = window.MediaStream;
                if (OriginalMediaStream) {
                    const self = this;
                    window.MediaStream = function(...args) {
                        const stream = new OriginalMediaStream(...args);

                        // Check if this is an audio stream
                        if (stream.getAudioTracks().length > 0) {
                            console.log('🎵 New MediaStream created with audio tracks:', stream.id);
                            setTimeout(() => {
                                self.processMediaStream(stream, 'MediaStream-constructor');
                            }, 100);
                        }

                        return stream;
                    };
                    window.MediaStream.prototype = OriginalMediaStream.prototype;
                }

                // Find existing streams
                this.findExistingMediaStreams();
            }

            findExistingMediaStreams() {
                console.log('Finding existing MediaStreams...');

                // Look for streams in various global objects
                const streamSources = [window, window.parent, window.top];

                streamSources.forEach(source => {
                    for (let key in source) {
                        try {
                            const obj = source[key];
                            if (obj instanceof MediaStream) {
                                const audioTracks = obj.getAudioTracks();
                                if (audioTracks.length > 0 && !this.processedStreams.has(obj.id)) {
                                    console.log('🎵 Found existing MediaStream:', key, obj.id);
                                    this.processMediaStream(obj, 'existing-' + key);
                                }
                            }
                        } catch (e) {
                            // Skip inaccessible properties
                        }
                    }
                });

                // Also check for streams in common BBB storage locations
                const commonLocations = [
                    'mediaStream', 'audioStream', 'stream',
                    '_stream', '_mediaStream', '_audioStream'
                ];

                commonLocations.forEach(location => {
                    try {
                        if (window[location] instanceof MediaStream) {
                            const stream = window[location];
                            if (stream.getAudioTracks().length > 0 && !this.processedStreams.has(stream.id)) {
                                console.log('🎵 Found MediaStream in common location:', location);
                                this.processMediaStream(stream, 'common-' + location);
                            }
                        }
                    } catch (e) {
                        // Skip
                    }
                });
            }

            processMediaStream(stream, source) {
                if (this.processedStreams.has(stream.id)) {
                    console.log('Stream already processed:', stream.id);
                    return;
                }
                this.processedStreams.add(stream.id);

                console.log('🎵 Processing MediaStream from:', source, stream.id, 'tracks:', stream.getAudioTracks().length);

                try {
                    if (!this.audioContext) {
                        this.audioContext = new (window.AudioContext || window.webkitAudioContext)({
                            sampleRate: 16000
                        });
                        console.log('AudioContext created with sample rate:', this.audioContext.sampleRate);
                    }

                    const sourceNode = this.audioContext.createMediaStreamSource(stream);
                    const processor = this.audioContext.createScriptProcessor(2048, 1, 1);

                    sourceNode.connect(processor);
                    processor.connect(this.audioContext.destination);

                    let packetCount = 0;
                    let lastLogTime = Date.now();
                    let silentCount = 0;

                    processor.onaudioprocess = (event) => {
                        const inputData = event.inputBuffer.getChannelData(0);

                        // Calculate audio level
                        let rms = 0;
                        let maxAmplitude = 0;
                        let hasAudio = false;

                        for (let i = 0; i < inputData.length; i++) {
                            const amplitude = Math.abs(inputData[i]);
                            rms += amplitude * amplitude;
                            if (amplitude > maxAmplitude) {
                                maxAmplitude = amplitude;
                            }
                            if (amplitude > 0.001) {
                                hasAudio = true;
                            }
                        }
                        rms = Math.sqrt(rms / inputData.length);

                        // Skip if silent
                        if (!hasAudio || rms < 0.0005) {
                            silentCount++;
                            if (silentCount % 100 === 0) {
                                console.log('Silent audio from', source, 'silent packets:', silentCount);
                            }
                            return;
                        }
                        silentCount = 0;

                        // Convert to 16-bit PCM
                        const pcmData = new Int16Array(inputData.length);
                        for (let i = 0; i < inputData.length; i++) {
                            pcmData[i] = Math.max(-32768, Math.min(32767, inputData[i] * 32768));
                        }

                        const byteArray = new Uint8Array(pcmData.buffer);

                        if (window.sendAudioData) {
                            window.sendAudioData(Array.from(byteArray));
                            packetCount++;

                            if (packetCount % 25 === 0 || Date.now() - lastLogTime > 5000) {
                                console.log('🎵 Audio packet', packetCount, 'from', source,
                                          'samples:', inputData.length,
                                          'rms:', rms.toFixed(6),
                                          'max:', maxAmplitude.toFixed(6));
                                lastLogTime = Date.now();
                            }
                        }
                    };

                    // Handle stream ending
                    stream.getAudioTracks().forEach(track => {
                        track.onended = () => {
                            console.log('Audio track ended:', source, track.id);
                            this.processedStreams.delete(stream.id);
                        };
                    });

                    console.log('✅ Successfully processing audio from:', source);

                } catch (error) {
                    console.error('Error processing audio stream:', error, 'source:', source);
                }
            }

            hookIntoAllAudioElements() {
                console.log('Hooking into ALL audio elements...');

                // Process existing audio elements
                const audioElements = document.querySelectorAll('audio');
                console.log('Found', audioElements.length, 'audio elements on main page');

                audioElements.forEach((audio, idx) => {
                    this.hookIntoAudioElement(audio, 'main-audio-' + idx);
                });

                // Monitor for new audio elements
                const observer = new MutationObserver((mutations) => {
                    mutations.forEach((mutation) => {
                        mutation.addedNodes.forEach((node) => {
                            if (node.nodeName === 'AUDIO') {
                                console.log('New audio element detected via mutation observer');
                                this.hookIntoAudioElement(node, 'new-audio-element');
                            }

                            // Also check if any added nodes contain audio elements
                            if (node.querySelectorAll) {
                                const childAudios = node.querySelectorAll('audio');
                                childAudios.forEach((audio, idx) => {
                                    this.hookIntoAudioElement(audio, 'child-audio-' + idx);
                                });
                            }
                        });
                    });
                });

                observer.observe(document.body, {
                    childList: true,
                    subtree: true
                });
            }

            hookIntoAudioElement(audioElement, source) {
                if (this.processedElements.has(audioElement)) {
                    return;
                }
                this.processedElements.add(audioElement);

                console.log('Hooking into audio element:', source);

                // Check if it already has a stream
                if (audioElement.srcObject && audioElement.srcObject.getAudioTracks().length > 0) {
                    console.log('Audio element already has srcObject:', source);
                    this.processMediaStream(audioElement.srcObject, source + '-existing-srcObject');
                }

                // Monitor for srcObject changes
                let currentSrcObject = audioElement.srcObject;
                const checkSrcObject = () => {
                    if (audioElement.srcObject && audioElement.srcObject !== currentSrcObject) {
                        currentSrcObject = audioElement.srcObject;
                        console.log('Audio element srcObject changed:', source, currentSrcObject.id);

                        if (currentSrcObject.getAudioTracks().length > 0) {
                            this.processMediaStream(currentSrcObject, source + '-changed-srcObject');
                        }
                    }

                    // Continue monitoring if element is still in DOM
                    if (audioElement.isConnected && !this.processedElements.has(audioElement)) {
                        setTimeout(checkSrcObject, 2000);
                    }
                };

                // Start monitoring
                checkSrcObject();

                // Also monitor play state
                audioElement.addEventListener('play', () => {
                    console.log('Audio element started playing:', source);
                    if (audioElement.srcObject && audioElement.srcObject.getAudioTracks().length > 0) {
                        this.processMediaStream(audioElement.srcObject, source + '-onplay');
                    }
                });
            }

            hookIntoWebRTCGlobally() {
                console.log('Hooking into WebRTC globally...');
                const self = this;

                // Override RTCPeerConnection
                if (window.RTCPeerConnection) {
                    const OriginalRTCPeerConnection = window.RTCPeerConnection;
                    window.RTCPeerConnection = function(...args) {
                        const pc = new OriginalRTCPeerConnection(...args);

                        // Monitor for tracks
                        pc.ontrack = (event) => {
                            console.log('RTCPeerConnection ontrack:', event.track.kind, event.track.id);
                            if (event.track.kind === 'audio') {
                                const stream = event.streams[0] || new MediaStream([event.track]);
                                setTimeout(() => {
                                    self.processMediaStream(stream, 'webrtc-ontrack');
                                }, 500);
                            }
                        };

                        // Override addTrack
                        const originalAddTrack = pc.addTrack;
                        pc.addTrack = (track, ...rest) => {
                            console.log('RTCPeerConnection addTrack:', track.kind, track.id);
                            if (track.kind === 'audio') {
                                const stream = new MediaStream([track]);
                                setTimeout(() => {
                                    self.processMediaStream(stream, 'webrtc-addtrack');
                                }, 500);
                            }
                            return originalAddTrack.call(pc, track, ...rest);
                        };

                        return pc;
                    };
                    window.RTCPeerConnection.prototype = OriginalRTCPeerConnection.prototype;
                }
            }

            startPeriodicDiscovery() {
                // Periodic discovery of new streams
                setInterval(() => {
                    console.log('Periodic audio discovery...');
                    this.findExistingMediaStreams();

                    // Check audio elements again
                    const audioElements = document.querySelectorAll('audio');
                    audioElements.forEach((audio, idx) => {
                        if (!this.processedElements.has(audio)) {
                            this.hookIntoAudioElement(audio, 'periodic-audio-' + idx);
                        }
                    });
                }, 10000);
            }
        }

        // Initialize universal capture
        window.universalBBBAudioCapture = new UniversalBBBAudioCapture();

        return {
            status: 'universal_audio_capture_started',
            method: 'comprehensive_monitoring',
            audioElements: document.querySelectorAll('audio').length
        };
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("Universal BBB audio capture failed: %v", err)
        return err
    }

    log.Printf("Universal BBB Audio Capture: %v", result)
    return nil
}

func (b *Bot) logTranscriptionToFile(text string) error {
    // Skip empty transcriptions
    if strings.TrimSpace(text) == "" {
        return nil
    }

    // Use /tmp which should always be writable
    filename := fmt.Sprintf("/tmp/%s_transcript.txt", b.MeetingID)

    // Prepare log entry with timestamp
    logEntry := fmt.Sprintf("[%s] %s\n",
        time.Now().Format("2006-01-02 15:04:05"),
        text)

    // Append to file
    file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return fmt.Errorf("failed to open transcript file: %v", err)
    }
    defer file.Close()

    if _, err := file.WriteString(logEntry); err != nil {
        return fmt.Errorf("failed to write to transcript file: %v", err)
    }

    log.Printf("📝 Transcription logged to file: %s", text)
    return nil
}

func (bac *BrowserAudioCapturer) debugPageStructure() error {
    log.Printf("Debugging page structure and contexts...")

    jsCode := `() => {
        console.log('=== PAGE STRUCTURE DEBUG ===');

        const debugInfo = {
            url: window.location.href,
            title: document.title,
            frames: window.frames.length,
            hasParent: window.parent !== window,
            hasTop: window.top !== window,

            // Check for common BBB indicators
            indicators: {
                hasHTML5Client: window.location.href.includes('html5client'),
                hasSessionToken: window.location.href.includes('sessionToken'),
                hasAudioElements: document.querySelectorAll('audio').length,
                hasVideoElements: document.querySelectorAll('video').length,
                hasCanvas: document.querySelectorAll('canvas').length
            },

            // Check global objects
            globals: {
                APP: !!window.APP,
                App: !!window.App,
                BBB: !!window.BBB,
                html5client: !!window.html5client,
                client: !!window.client,
                MediaStream: !!window.MediaStream,
                RTCPeerConnection: !!window.RTCPeerConnection,
                AudioContext: !!(window.AudioContext || window.webkitAudioContext)
            }
        };

        // Check frames
        debugInfo.frameInfo = [];
        try {
            for (let i = 0; i < window.frames.length; i++) {
                try {
                    const frame = window.frames[i];
                    debugInfo.frameInfo.push({
                        index: i,
                        url: frame.location.href,
                        hasAPP: !!(frame.APP || frame.App || frame.BBB)
                    });
                } catch (e) {
                    debugInfo.frameInfo.push({
                        index: i,
                        error: 'cross-origin'
                    });
                }
            }
        } catch (e) {
            debugInfo.frameError = e.toString();
        }

        console.log('Page Structure:', debugInfo);
        return debugInfo;
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("Page structure debug failed: %v", err)
        return err
    }

    log.Printf("Page Structure Debug: %v", result)
    return nil
}

func (bac *BrowserAudioCapturer) StartAudioCapture(transcriptionHost string, transcriptionPort int) {
    log.Printf("Starting Universal BBB Audio Capture...")

    audioPort := 5001
    if err := bac.setupAudioStreaming(transcriptionHost, audioPort); err != nil {
        log.Printf("Failed to setup audio streaming: %v", err)
        return
    }

    if err := bac.verifySendAudioFunction(); err != nil {
        log.Printf("sendAudioData verification failed: %v", err)
        return
    }

    // Wait longer for BBB to fully initialize
    time.Sleep(15 * time.Second)

    if err := bac.debugPageStructure(); err != nil {
    log.Printf("Page structure debug failed: %v", err)
    }

    // Use universal capture approach
    if err := bac.captureBBBUniversalAudio(); err != nil {
        log.Printf("Universal audio capture failed: %v", err)

        // Fallback to test tone
        log.Printf("Starting test tone as fallback...")
        bac.sendContinuousTestTone()
    }

    // Start monitoring
    go bac.startAudioMonitoring()

    log.Printf("✅ Universal BBB Audio Capture started!")
}

func (bac *BrowserAudioCapturer) debugAudioConsumers() error {
    log.Printf("Debugging BBB audio consumers...")

    jsCode := `() => {
        if (!window.APP || !window.APP.conference || !window.APP.conference._room) {
            return { error: "Conference room not available" };
        }

        const room = window.APP.conference._room;
        const debugInfo = {
            audioConsumers: room._audioConsumers ? room._audioConsumers.size : 0,
            consumers: []
        };

        if (room._audioConsumers) {
            room._audioConsumers.forEach((consumer, id) => {
                const consumerInfo = {
                    id: id,
                    kind: consumer.kind,
                    paused: consumer.paused,
                    closed: consumer.closed,
                    hasTrack: !!consumer._track,
                    trackReadyState: consumer._track ? consumer._track.readyState : 'no-track',
                    trackEnabled: consumer._track ? consumer._track.enabled : false,
                    trackMuted: consumer._track ? consumer._track.muted : false
                };
                debugInfo.consumers.push(consumerInfo);
                console.log('Audio Consumer:', consumerInfo);
            });
        }

        // Check user list
        if (room._users) {
            debugInfo.users = room._users.size;
            debugInfo.userList = [];
            room._users.forEach((user, id) => {
                debugInfo.userList.push({
                    id: id,
                    name: user.name,
                    voice: user.voiceUser ? user.voiceUser.joined : false
                });
            });
        }

        return debugInfo;
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("Audio consumers debug failed: %v", err)
        return err
    }

    log.Printf("BBB Audio Consumers: %v", result)
    return nil
}


func (bac *BrowserAudioCapturer) startAudioMonitoring() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for bac.isConnected {
        <-ticker.C
        bac.CheckAudioStatus()
    }
}

func (bac *BrowserAudioCapturer) verifyAudioPlayback() error {
    log.Printf("Verifying audio playback in BBB...")

    jsCode := `() => {
        console.log('[BBB-Bot] Verifying audio playback');

        // Check if any audio elements are actually playing sound
        const audioElements = document.querySelectorAll('audio');
        let playingAudios = 0;
        let mutedAudios = 0;
        let pausedAudios = 0;

        audioElements.forEach((audio, idx) => {
            // Create a temporary analyzer to check if audio is playing
            if (!audio.paused && !audio.muted && audio.readyState > 0) {
                try {
                    const audioContext = new (window.AudioContext || window.webkitAudioContext)();
                    const source = audioContext.createMediaElementSource(audio);
                    const analyzer = audioContext.createAnalyser();

                    source.connect(analyzer);
                    analyzer.connect(audioContext.destination);

                    // Check for audio data
                    const dataArray = new Uint8Array(analyzer.frequencyBinCount);
                    analyzer.getByteFrequencyData(dataArray);

                    let hasAudio = false;
                    for (let i = 0; i < dataArray.length; i++) {
                        if (dataArray[i] > 10) { // Threshold for actual audio
                            hasAudio = true;
                            break;
                        }
                    }

                    if (hasAudio) {
                        console.log(` + "`" + `🎵 Audio element ${idx} is PLAYING actual audio` + "`" + `);
                        playingAudios++;
                    } else {
                        console.log(` + "`" + `🔇 Audio element ${idx} is playing but SILENT` + "`" + `);
                    }

                    audioContext.close();
                } catch (e) {
                    console.log(` + "`" + `❌ Could not analyze audio element ${idx}: ${e}` + "`" + `);
                }
            } else {
                if (audio.muted) mutedAudios++;
                if (audio.paused) pausedAudios++;
            }
        });

        console.log(` + "`" + `Audio Summary: ${playingAudios} playing, ${mutedAudios} muted, ${pausedAudios} paused` + "`" + `);

        return {
            playing: playingAudios,
            muted: mutedAudios,
            paused: pausedAudios,
            total: audioElements.length
        };
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("Audio playback verification failed: %v", err)
        return err
    }

    log.Printf("Audio Playback Verification: %v", result)
    return nil
}



func (bac *BrowserAudioCapturer) debugAudioElementsInDetail() error {
    log.Printf("Debugging audio elements in detail...")

    jsCode := `() => {
        console.log('[BBB-Bot] Detailed audio element analysis');

        const audioElements = document.querySelectorAll('audio');
        const detailedInfo = {
            total: audioElements.length,
            elements: []
        };

        audioElements.forEach((audio, idx) => {
            const elementInfo = {
                index: idx,
                paused: audio.paused,
                muted: audio.muted,
                volume: audio.volume,
                readyState: audio.readyState,
                duration: audio.duration,
                currentTime: audio.currentTime,
                src: audio.src ? 'HAS_SRC' : 'NO_SRC',
                currentSrc: audio.currentSrc ? audio.currentSrc.substring(0, 100) : 'NO_CURRENT_SRC',
                srcObject: !!audio.srcObject,
                networkState: audio.networkState,
                error: audio.error ? audio.error.message : 'NO_ERROR'
            };

            // Try to get more info if srcObject exists
            if (audio.srcObject) {
                const stream = audio.srcObject;
                elementInfo.srcObjectDetails = {
                    id: stream.id,
                    active: stream.active,
                    audioTracks: stream.getAudioTracks().map(track => ({
                        id: track.id,
                        kind: track.kind,
                        enabled: track.enabled,
                        muted: track.muted,
                        readyState: track.readyState,
                        label: track.label
                    }))
                };
            }

            console.log('Audio Element ' + idx + ':', elementInfo);
            detailedInfo.elements.push(elementInfo);
        });

        // Check if we're in listen-only mode by looking for UI indicators
        const listenOnlyIndicators = [
            document.querySelector('[data-test="listenOnly"]'),
            document.querySelector('[aria-label*="listen only"]'),
            document.querySelector('.listen-only'),
            document.querySelector('.audio-listen-only')
        ].filter(Boolean);

        detailedInfo.listenOnlyMode = listenOnlyIndicators.length > 0;
        detailedInfo.listenOnlyIndicators = listenOnlyIndicators.length;

        console.log('Detailed Audio Analysis:', detailedInfo);
        return detailedInfo;
    }`

    result, err := bac.page.Eval(jsCode)
    if err != nil {
        log.Printf("Detailed audio analysis failed: %v", err)
        return err
    }

    log.Printf("Detailed Audio Analysis: %v", result)
    return nil
}

func (bac *BrowserAudioCapturer) sendContinuousTestTone() {
        log.Printf("Sending continuous test tone for pipeline verification...")

        go func() {
                sampleRate := 16000
                packetCount := 0

                for bac.isConnected {
                        testAudio := generateTestTone()
                        if bac.audioConn != nil {
                                _, err := bac.audioConn.Write(testAudio)
                                if err != nil {
                                        log.Printf("Test tone send error: %v", err)
                                        break
                                }
                                packetCount++

                                if packetCount%50 == 0 {
                                        log.Printf("✅ Sent test tone packet %d (%d bytes)", packetCount, len(testAudio))
                                }
                        }

                        // Calculate proper sleep time for 16kHz sample rate
                        samplesPerPacket := len(testAudio) / 2 // 2 bytes per sample
                        sleepTime := time.Duration(samplesPerPacket) * time.Second / time.Duration(sampleRate)
                        time.Sleep(sleepTime)
                }
        }()
}

func (bac *BrowserAudioCapturer) CheckAudioStatus() {
        log.Printf("Performing audio element check")

        // Simple element counting
        audios, _ := bac.page.Elements("audio")
        videos, _ := bac.page.Elements("video")

        log.Printf("Audio status - Audio: %d, Video: %d", len(audios), len(videos))
}

func (bac *BrowserAudioCapturer) debugPageContent() {
        log.Printf("Debugging page content...")

        // Get page URL
        url := bac.page.MustInfo().URL
        log.Printf("Page URL: %s", url)

        // Get page title using JavaScript
        titleScript := `() => document.title`
        titleResult, err := bac.page.Eval(titleScript)
        if err == nil && titleResult != nil {
                title := titleResult.Value.String()
                log.Printf("Page title: %s", title)
        }

        // Count visible elements
        buttons, _ := bac.page.Elements("button")
        inputs, _ := bac.page.Elements("input")
        log.Printf("Found %d buttons and %d inputs on page", len(buttons), len(inputs))

        // Take screenshot
        screenshotBytes, err := bac.page.Screenshot(false, nil)
        if err != nil {
                log.Printf("Failed to capture screenshot: %v", err)
                return
        }

        err = os.WriteFile("debug_page.png", screenshotBytes, 0644)
        if err != nil {
                log.Printf("Failed to write screenshot: %v")
        } else {
                log.Printf("Screenshot saved as debug_page.png")
        }
}

func (bac *BrowserAudioCapturer) JoinMeeting(meetingID, moderatorPW, userName string) error {
        log.Printf("Starting Greenlight-based meeting join for %s", meetingID)

        if err := bac.setupBrowser(); err != nil {
                return fmt.Errorf("failed to setup browser: %v", err)
        }

        if err := bac.joinBBBMeeting(meetingID, moderatorPW, userName); err != nil {
                return fmt.Errorf("failed to join meeting: %v", err)
        }

        // Handle the Greenlight join flow
        bac.handleBBBJoinFlow()

        bac.isConnected = true
        log.Printf("Successfully joined meeting %s through Greenlight", meetingID)
        return nil
}

func (bac *BrowserAudioCapturer) Disconnect() {
        bac.isConnected = false

        if bac.audioConn != nil {
                bac.audioConn.Close()
                log.Printf("Audio connection closed")
        }

        if bac.browser != nil {
                bac.browser.MustClose()
        }

        log.Printf("Browser audio capturer disconnected")
}

func generateTestTone() []byte {
        // Generate a simple 440Hz sine wave
        samples := 8192
        sampleRate := 16000
        freq := 440.0
        data := make([]byte, samples*2)

        for i := 0; i < samples; i++ {
                value := int16(math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)) * 32767)
                data[i*2] = byte(value & 0xFF)
                data[i*2+1] = byte((value >> 8) & 0xFF)
        }
        return data
}

// PadCapture handles text capture functionality
type PadCapture struct {
        Language    string
        External    bool
        Host        string
        Port        int
        IsConnected bool
        padBaseURL  string
        apiKey      string
}

func NewPadCapture(language string, external bool, host string, port int) *PadCapture {
         padBaseURL := os.Getenv("BBB_PAD_URL")
         apiKey := os.Getenv("ETHERPAD_API_KEY")


        return &PadCapture{
                Language:    language,
                External:    external,
                Host:        host,
                Port:        port,
                IsConnected: true,
                padBaseURL:  padBaseURL,
                apiKey:      apiKey,
        }
}

func (p *PadCapture) SetText(text string) error {
    if !p.IsConnected {
        return fmt.Errorf("pad capture not connected")
    }

    log.Printf("Setting text for %s pad: %s", p.Language, text[:min(50, len(text))])

    // Use official Etherpad API
    padID := fmt.Sprintf("bbb-transcription-%s", p.Language)

    // First, create the pad if it doesn't exist
    if err := p.createPadIfNotExists(padID); err != nil {
        return fmt.Errorf("failed to ensure pad exists: %v", err)
    }

    // Then set the text
    if err := p.setTextViaEtherpadAPI(padID, text); err != nil {
        return fmt.Errorf("failed to set text via etherpad API: %v", err)
    }

    log.Printf("✅ Successfully sent text to pad: %s", padID)
    return nil
}

func (p *PadCapture) createPadIfNotExists(padID string) error {
    // Check if pad exists
    checkURL := fmt.Sprintf("%sapi/1.2.15/getText?apikey=%s&padID=%s",
        p.padBaseURL, p.apiKey, padID)

    resp, err := http.Get(checkURL)
    if err == nil && resp.StatusCode == http.StatusOK {
        resp.Body.Close()
        return nil // Pad exists
    }

    // Create pad
    createURL := fmt.Sprintf("%sapi/1.2.15/createPad?apikey=%s&padID=%s&text=%s",
                           p.padBaseURL, p.apiKey, padID, url.QueryEscape("BBB Transcription Bot - Live Session"))

    resp, err = http.Get(createURL)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        body, _ := ioutil.ReadAll(resp.Body)
        return fmt.Errorf("failed to create pad: %s - %s", resp.Status, string(body))
    }

    log.Printf("📝 Created new pad: %s", padID)
    return nil
}

func (p *PadCapture) setTextViaEtherpadAPI(padID, text string) error {
    // Use setText API endpoint
    setTextURL := fmt.Sprintf("%sapi/1.2.15/setText?apikey=%s&padID=%s&text=%s",
        p.padBaseURL, p.apiKey, padID, url.QueryEscape(text))

    resp, err := http.Get(setTextURL)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    // Read response body to check for API errors
    body, _ := ioutil.ReadAll(resp.Body)

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("etherpad API error: %s - %s", resp.Status, string(body))
    }

    // Check if response indicates success
    var result map[string]interface{}
    if err := json.Unmarshal(body, &result); err == nil {
        if code, exists := result["code"]; exists && code.(float64) != 0 {
            return fmt.Errorf("etherpad API returned error: %v", result)
        }
    }

    log.Printf("📝 Text set successfully in pad: %s", padID)
    return nil
}

func (p *PadCapture) Disconnect() {
        p.IsConnected = false
}

// Bot represents a translation bot
type Bot struct {
        ID              string     `json:"id"`
        Status          StatusType `json:"status"`
        client          *BBBClient
        clients         map[string]*BBBClient
        captures        map[string]*PadCapture
        Sub_bots        int      `json:"sub_bots"`
        Languages       []string `json:"languages"`
        clientsMutex    sync.Mutex
        streamclient    *StreamClient
        en_caption      *PadCapture
        browserCapturer *BrowserAudioCapturer
        useBrowserAudio bool

        bbb_client_url         string
        bbb_client_ws          string
        bbb_pad_url            string
        bbb_pad_ws             string
        bbb_api_url            string
        bbb_api_secret         string
        bbb_webrtc_ws          string
        transcription_host     string
        transcription_port     int
        transcription_secret   string
        translation_server_url string
        changeset_external     bool
        changeset_port         int
        changeset_host         string
        Task                   Task `json:"task"`

        MeetingID   string `json:"meeting_id"`
        UserName    string `json:"user_name"`
        ModeratorPW string `json:"-"`
}

func NewBot(
        bbb_client_url string,
        bbb_client_ws string,
        bbb_pad_url string,
        bbb_pad_ws string,
        bbb_api_url string,
        bbb_api_secret string,
        bbb_webrtc_ws string,
        transcription_host string,
        transcription_port int,
        transcription_secret string,
        translation_server_url string,
        changeset_external bool,
        changeset_port int,
        changeset_host string,
        task Task,
) *Bot {
        meetingID := os.Getenv("MEETING_ID")
        moderatorPW := os.Getenv("MODERATOR_PW")
        botName := os.Getenv("BOT_NAME")

        if botName == "" {
                botName = "TranslationBot"
        }

        return_bot := &Bot{
                ID:               uuid.New().String(),
                Status:           Disconnected,
                Task:             task,
                clients:          make(map[string]*BBBClient),
                captures:         make(map[string]*PadCapture),
                Languages:        make([]string, 0),
                streamclient:     NewStreamClient(transcription_host, transcription_port, true, transcription_secret),
                en_caption:       NewPadCapture("en", changeset_external, changeset_host, changeset_port),
                useBrowserAudio:  true,
                bbb_client_url:   bbb_client_url,
                bbb_client_ws:    bbb_client_ws,
                bbb_pad_url:      bbb_pad_url,
                bbb_pad_ws:       bbb_pad_ws,
                bbb_api_url:      bbb_api_url,
                bbb_api_secret:   bbb_api_secret,
                bbb_webrtc_ws:    bbb_webrtc_ws,
                transcription_host:     transcription_host,
                transcription_port:     transcription_port,
                transcription_secret:   transcription_secret,
                translation_server_url: translation_server_url,
                changeset_port:         changeset_port,
                changeset_host:         changeset_host,
                changeset_external:     changeset_external,

                MeetingID:   meetingID,
                UserName:    botName,
                ModeratorPW: moderatorPW,
        }

        return_bot.client = NewBBBClient(bbb_api_url, bbb_api_secret, bbb_client_url, bbb_webrtc_ws, return_bot)
        return_bot.Languages = append(return_bot.Languages, "en")

        // Initialize browser audio capturer
        if return_bot.useBrowserAudio {
                return_bot.browserCapturer = NewBrowserAudioCapturer()
        }

        log.Printf("Bot created with MeetingID: %s", meetingID)
        return return_bot
}

func (b *Bot) Join(meetingID, userName string) error {
	meetingID = os.Getenv("MEETING_ID")
	if meetingID == "" {
                return fmt.Errorf("meetingID is required")
        }
	moderatorPW := os.Getenv("MODERATOR_PW")
        if moderatorPW == "" {
                return fmt.Errorf("moderatorPW is required")
        }
	userName = os.Getenv("BOT_NAME")
        if userName == "" {
                userName = b.UserName
        }

        b.MeetingID = meetingID
        b.ModeratorPW = moderatorPW
        b.UserName = userName

        if b.Status == Connecting {
                return fmt.Errorf("already connecting")
        }

        if b.Status == Connected {
                b.Disconnect()
        }
        b.Status = Connecting

        log.Printf("Joining meeting %s as %s", b.MeetingID, b.UserName)

        // Test transcription service connection BEFORE joining meeting
        if err := b.testTranscriptionConnection(); err != nil {
                b.Status = Disconnected
                return fmt.Errorf("transcription service unavailable: %v", err)
        }

        if b.useBrowserAudio {
                // Use browser-based audio capture
                if err := b.browserCapturer.JoinMeeting(b.MeetingID, b.ModeratorPW, b.UserName); err != nil {
                        b.Status = Disconnected
                        return fmt.Errorf("failed to join meeting with browser: %v", err)
                }

                // Set up StreamClient callbacks FIRST
                b.streamclient.OnConnected(func(message string) {
                        log.Println("Connected to transcription server.")

                        // Start audio processing after successful connection
                        go func() {
                                time.Sleep(3 * time.Second) // Wait a bit for everything to stabilize

                                // Use environment variables directly for audio capture
                                host := os.Getenv("TRANSCRIPTION_HOST")
                                port := 5001 // Use UDP port for audio streaming

                                // Start audio capture and streaming
                                b.browserCapturer.StartAudioCapture(host, port)

                                // Verify audio capture after setup
                                time.Sleep(5 * time.Second)
                                b.browserCapturer.CheckAudioStatus()
                        }()
                })

                b.streamclient.OnDisconnected(func(message string) {
                        log.Println("Disconnected from transcription server.")
                        b.Disconnect()
                })

                b.streamclient.OnTCPMessage(func(text string) {
                        b.handleTranscription(text)
                })

                // Connect using the existing StreamClient
                if err := b.streamclient.Connect(); err != nil {
                        b.Status = Disconnected
                        return fmt.Errorf("failed to connect to transcription: %v", err)
                }

        } else {
                // Use traditional WebSocket approach
                if err := b.client.JoinMeeting(b.MeetingID, b.ModeratorPW, b.UserName); err != nil {
                        b.Status = Disconnected
                        return fmt.Errorf("failed to join meeting: %v", err)
                }
        }

        b.Status = Connected
        log.Printf("Successfully joined meeting %s with browser audio", b.MeetingID)

        // Test pad integration
        b.TestPadIntegration()

        return nil
}

func (b *Bot) TestPadIntegration() {
    log.Printf("🧪 Testing pad integration...")
    log.Printf("Pad URL: %s", b.bbb_pad_url)
    log.Printf("Changeset: %s:%d", b.changeset_host, b.changeset_port)

    testText := "Pad integration test at " + time.Now().Format("15:04:05")

    if err := b.en_caption.SetText(testText); err != nil {
        log.Printf("❌ Pad test failed: %v", err)
    } else {
        log.Printf("✅ Pad test passed - check https://visio.poste.tn/pad/transcription-en")
    }
}

// Helper function to test transcription service connection
func (b *Bot) testTranscriptionConnection() error {
        // Always use environment variables directly
        host := os.Getenv("TRANSCRIPTION_HOST")
        portStr := os.Getenv("TRANSCRIPTION_PORT")

        // Add debug logging
        log.Printf("DEBUG: Environment transcription config - host: '%s', port: '%s'", host, portStr)

        if host == "" {
                host = "transcription-service" // default
                log.Printf("Using default transcription host: %s", host)
        }

        port := 5000
        if portStr != "" {
                if p, err := strconv.Atoi(portStr); err == nil {
                        port = p
                } else {
                        log.Printf("Warning: Invalid TRANSCRIPTION_PORT '%s', using default 5000", portStr)
                }
        }

        address := fmt.Sprintf("%s:%d", host, port)
        log.Printf("Testing connection to transcription service at %s", address)

        conn, err := net.DialTimeout("tcp", address, 5*time.Second)
        if err != nil {
                return fmt.Errorf("cannot connect to transcription service at %s: %v", address, err)
        }
        conn.Close()
        log.Printf("Transcription service connection test passed to %s", address)
        return nil
}

func (b *Bot) Translate(targetLang string) error {
        if b.Status != Connected {
                return fmt.Errorf("bot is not connected")
        }

        if b.Task != TaskTranslate {
                return fmt.Errorf("bot is not in translate mode")
        }

        if _, ok := b.clients[targetLang]; ok {
                return fmt.Errorf("language %s already active", targetLang)
        }

        newClient := NewBBBClient(b.bbb_api_url, b.bbb_api_secret, b.bbb_client_url, b.bbb_webrtc_ws, b)
        if err := newClient.JoinMeeting(b.MeetingID, b.ModeratorPW, b.UserName+"-"+targetLang); err != nil {
                return fmt.Errorf("failed to join meeting for %s: %v", targetLang, err)
        }

        newCapture := NewPadCapture(targetLang, b.changeset_external, b.changeset_host, b.changeset_port)

        b.clientsMutex.Lock()
        b.clients[targetLang] = newClient
        b.captures[targetLang] = newCapture
        b.Languages = append(b.Languages, targetLang)
        b.clientsMutex.Unlock()

        return nil
}

func (b *Bot) handleTranscription(text string) {
    validtext := strings.ToValidUTF8(text, "")

    // ADD THIS LINE - Log to file
    if err := b.logTranscriptionToFile(validtext); err != nil {
        log.Printf("Failed to log transcription to file: %v", err)
    }

    // Existing functionality
    if b.Task == TaskTranscribe {
        b.en_caption.SetText(validtext)
    } else if b.Task == TaskTranslate {
        b.en_caption.SetText(validtext)
        b.translateToAllLanguages(validtext)
    }
}


func (b *Bot) translateToAllLanguages(text string) {
        b.clientsMutex.Lock()
        defer b.clientsMutex.Unlock()

        for lang := range b.clients {
                if lang != "en" {
                        translatedText, err := translate(b.translation_server_url, text, "en", lang)
                        if err != nil {
                                log.Printf("Translation error for %s: %v", lang, err)
                                continue
                        }
                        if capture, ok := b.captures[lang]; ok {
                                capture.SetText(translatedText)
                        }
                }
        }
}

func (b *Bot) Disconnect() {
        if b.streamclient != nil {
                b.streamclient.Close()
        }
        if b.client != nil {
                b.client.Leave()
        }
        if b.browserCapturer != nil {
                b.browserCapturer.Disconnect()
        }
        b.Status = Disconnected

        b.clientsMutex.Lock()
        for lang, client := range b.clients {
                client.Leave()
                delete(b.clients, lang)
        }
        for lang := range b.captures {
                delete(b.captures, lang)
        }
        b.clientsMutex.Unlock()
}

func (b *Bot) DebugAudioStatus() {
        log.Printf("Audio Status Debug:")
        if b.useBrowserAudio {
                log.Printf("   Browser Audio: Active")
                log.Printf("   Browser Connected: %v", b.browserCapturer.isConnected)
        } else {
                log.Printf("   WebSocket Connected: %v", b.client.IsConnected)
                log.Printf("   Voice Bridge: %s", b.client.VoiceBridge)
                log.Printf("   Consumers: %d", len(b.client.consumers))
        }
        log.Printf("   Bot Status: %v", b.Status)
}

func (b *Bot) StopTranslate(targetLang string) error {
        if targetLang == "en" {
                b.SetTask(TaskTranscribe)
                return nil
        }

        b.clientsMutex.Lock()
        defer b.clientsMutex.Unlock()

        if client, ok := b.clients[targetLang]; ok {
                client.Leave()
                delete(b.clients, targetLang)
                delete(b.captures, targetLang)

                for i, lang := range b.Languages {
                        if lang == targetLang {
                                b.Languages = append(b.Languages[:i], b.Languages[i+1:]...)
                                break
                        }
                }
                return nil
        }
        return fmt.Errorf("translation for %s not found", targetLang)
}

func (b *Bot) GetTask() Task {
        return b.Task
}

func (b *Bot) SetTask(task Task) {
        if task == b.Task {
                return
        }

        if task == TaskTranscribe {
                b.stopAllTranslations()
        } else if task == TaskTranslate {
                b.startAllTranslations()
        }

        b.Task = task
}

func (b *Bot) stopAllTranslations() {
        b.clientsMutex.Lock()
        defer b.clientsMutex.Unlock()

        for lang, client := range b.clients {
                if lang != "en" {
                        client.Leave()
                        delete(b.clients, lang)
                        delete(b.captures, lang)
                }
        }

        var newLanguages []string
        for _, lang := range b.Languages {
                if lang == "en" {
                        newLanguages = append(newLanguages, lang)
                }
        }
        b.Languages = newLanguages
}

func (b *Bot) startAllTranslations() {
        for _, lang := range b.Languages {
                if lang != "en" {
                        if err := b.Translate(lang); err != nil {
                                log.Printf("Failed to start translation for %s: %v", lang, err)
                        }
                }
        }
}

// Helper function
func min(a, b int) int {
        if a < b {
                return a
        }
        return b
}


