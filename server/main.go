package main

import (
	"context"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"github.com/miekg/dns"
	"google.golang.org/genai"
)

type PayloadUP struct {
	Type     string `json:"type"` // "user" or "tool_response"
	Content  string `json:"content"`
	ToolName string `json:"tool_name,omitempty"`
}

type PayloadDOWN struct {
	Type     string         `json:"type"` // "text" or "tool_call"
	Content  string         `json:"content"`
	ToolName string         `json:"tool_name,omitempty"`
	ToolArgs map[string]any `json:"tool_args,omitempty"`
}

type PendingPrompt struct {
	Chunks map[int]string
}

type MessageResponse struct {
	Chunks []string
	Ready  bool
	Failed bool
}

type Session struct {
	ID             string
	ChatSession    *genai.Chat
	GenaiClient    *genai.Client
	PendingPrompts map[int]*PendingPrompt
	Responses      map[int]*MessageResponse
}

var (
	sessions = make(map[string]*Session)
	mu       sync.Mutex
)

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func handleQuery(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)

	if len(r.Question) == 0 {
		w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	name := strings.ToLower(q.Name)

	if !strings.HasSuffix(name, "llm.local.") {
		m.SetRcode(r, dns.RcodeNameError)
		w.WriteMsg(m)
		return
	}

	parts := strings.Split(name, ".")
	// Example: init.llm.local.
	if parts[0] == "init" {
		mu.Lock()
		id := generateID()
		ctx := context.Background()
		client, err := genai.NewClient(ctx, &genai.ClientConfig{})
		if err != nil {
			log.Errorf("Failed to init genai client: %v", err)
			m.SetRcode(r, dns.RcodeServerFailure)
			mu.Unlock()
			w.WriteMsg(m)
			return
		}

		config := &genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{
					genai.NewPartFromText("You are a remote, DNS-based AI agent. You have the ability to execute terminal commands on the user's machine by calling the client_execute_bash tool. You can also read files using client_read_file. When the user asks you to do something to their local system, utilize your tools. Format all outputs nicely."),
				},
			},
			Tools: []*genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "client_execute_bash",
							Description: "Executes a bash command on the client's local machine and returns the stdout/stderr.",
							Parameters: &genai.Schema{
								Type: genai.TypeObject,
								Properties: map[string]*genai.Schema{
									"command": {
										Type:        genai.TypeString,
										Description: "The bash command to execute.",
									},
								},
								Required: []string{"command"},
							},
						},
						{
							Name:        "client_read_file",
							Description: "Reads the contents of a specified file on the client's local machine.",
							Parameters: &genai.Schema{
								Type: genai.TypeObject,
								Properties: map[string]*genai.Schema{
									"filepath": {
										Type:        genai.TypeString,
										Description: "The absolute or relative path to the file to read.",
									},
								},
								Required: []string{"filepath"},
							},
						},
					},
				},
			},
		}

		chat, err := client.Chats.Create(ctx, "gemini-3.1-pro-preview", config, nil)
		if err != nil {
			log.Errorf("Failed to create chat: %v", err)
			m.SetRcode(r, dns.RcodeServerFailure)
			mu.Unlock()
			w.WriteMsg(m)
			return
		}

		sessions[id] = &Session{
			ID:             id,
			ChatSession:    chat,
			GenaiClient:    client,
			PendingPrompts: make(map[int]*PendingPrompt),
			Responses:      make(map[int]*MessageResponse),
		}
		mu.Unlock()

		addTXT(m, q.Name, id)
		w.WriteMsg(m)
		return
	}

	// Upload Chunk Format: <chunk>.<seq>.up.<msgID>.<id>.llm.local.
	// Parts: chunk, seq, "up", msgID, id, "llm", "local", ""
	if len(parts) >= 7 && parts[2] == "up" {
		chunkBase32 := parts[0]
		seq, err1 := strconv.Atoi(parts[1])
		msgID, err2 := strconv.Atoi(parts[3])
		id := parts[4]

		if err1 != nil || err2 != nil {
			m.SetRcode(r, dns.RcodeFormatError)
			w.WriteMsg(m)
			return
		}

		mu.Lock()
		sess, ok := sessions[id]
		if ok {
			if sess.PendingPrompts[msgID] == nil {
				sess.PendingPrompts[msgID] = &PendingPrompt{Chunks: make(map[int]string)}
			}
			sess.PendingPrompts[msgID].Chunks[seq] = chunkBase32
		}
		mu.Unlock()

		if ok {
			addTXT(m, q.Name, "ACK")
		} else {
			addTXT(m, q.Name, "ERR:NOSESSION")
		}
		w.WriteMsg(m)
		return
	}

	// Finish Upload Format: fin.<msgID>.<id>.llm.local.
	if parts[0] == "fin" && len(parts) >= 5 {
		msgID, err := strconv.Atoi(parts[1])
		id := parts[2]
		
		if err != nil {
			m.SetRcode(r, dns.RcodeFormatError)
			w.WriteMsg(m)
			return
		}

		mu.Lock()
		sess, ok := sessions[id]
		if ok {
			if sess.Responses[msgID] == nil {
				sess.Responses[msgID] = &MessageResponse{Ready: false}
			}
		}
		mu.Unlock()

		if ok {
			go processLLM(sess, msgID)
			addTXT(m, q.Name, "ACK")
		} else {
			addTXT(m, q.Name, "ERR:NOSESSION")
		}
		w.WriteMsg(m)
		return
	}

	// Download Format: <seq>.<msgID>.down.<id>.llm.local.
	if len(parts) >= 6 && parts[2] == "down" {
		seq, err1 := strconv.Atoi(parts[0])
		msgID, err2 := strconv.Atoi(parts[1])
		id := parts[3]
		
		if err1 != nil || err2 != nil {
			m.SetRcode(r, dns.RcodeFormatError)
			w.WriteMsg(m)
			return
		}

		mu.Lock()
		sess, ok := sessions[id]
		var responseChunk string
		var stateStr string
		
		if ok {
			msgResp := sess.Responses[msgID]
			if msgResp == nil || !msgResp.Ready {
				if msgResp != nil && msgResp.Failed {
					stateStr = "ERR:API_FAILED"
				} else {
					stateStr = "PENDING"
				}
			} else {
				if seq < len(msgResp.Chunks) {
					responseChunk = msgResp.Chunks[seq]
				} else {
					stateStr = "EOF"
				}
			}
		} else {
			stateStr = "ERR:NOSESSION"
		}
		mu.Unlock()

		if responseChunk != "" {
			addTXT(m, q.Name, responseChunk)
		} else {
			addTXT(m, q.Name, stateStr)
		}
		w.WriteMsg(m)
		return
	}

	m.SetRcode(r, dns.RcodeNameError)
	w.WriteMsg(m)
}

func addTXT(m *dns.Msg, name, txt string) {
	rr, err := dns.NewRR(fmt.Sprintf("%s 0 IN TXT \"%s\"", name, txt))
	if err == nil {
		m.Answer = append(m.Answer, rr)
	}
}

func processLLM(sess *Session, msgID int) {
	mu.Lock()
	promptData := sess.PendingPrompts[msgID]
	mu.Unlock()

	if promptData == nil {
		markFailed(sess, msgID)
		return
	}

	maxSeq := -1
	for k := range promptData.Chunks {
		if k > maxSeq {
			maxSeq = k
		}
	}

	var b32str strings.Builder
	for i := 0; i <= maxSeq; i++ {
		b32str.WriteString(promptData.Chunks[i])
	}

	decoder := base32.StdEncoding.WithPadding(base32.NoPadding)
	payloadBytes, err := decoder.DecodeString(strings.ToUpper(b32str.String()))
	if err != nil {
		log.Error("Base32 decode error", "session", sess.ID, "msgID", msgID, "err", err)
		markFailed(sess, msgID)
		return
	}

	var payloadUP PayloadUP
	err = json.Unmarshal(payloadBytes, &payloadUP)
	if err != nil {
		log.Error("JSON unmarshal error", "session", sess.ID, "msgID", msgID, "err", err, "body", string(payloadBytes))
		markFailed(sess, msgID)
		return
	}

	log.Info("Processing payload", "session", sess.ID, "msgID", msgID, "type", payloadUP.Type)

	ctx := context.Background()
	var resp *genai.GenerateContentResponse

	if payloadUP.Type == "user" {
		resp, err = sess.ChatSession.SendMessage(ctx, *genai.NewPartFromText(payloadUP.Content))
	} else if payloadUP.Type == "tool_response" {
		// Create a Map map[string]any for the tool response
        var responseContent map[string]any
        errJson := json.Unmarshal([]byte(payloadUP.Content), &responseContent)
        
        var functionResponse map[string]any
        if errJson == nil {
            functionResponse = responseContent
        } else {
            // Default to string map
            functionResponse = map[string]any{
                "result": payloadUP.Content,
            }
        }
		
		toolRespPart := genai.NewPartFromFunctionResponse(payloadUP.ToolName, functionResponse)
		resp, err = sess.ChatSession.SendMessage(ctx, *toolRespPart)
	} else {
		log.Error("Unknown payload type", "session", sess.ID, "msgID", msgID, "type", payloadUP.Type)
		markFailed(sess, msgID)
		return
	}

	if err != nil {
		log.Error("Chat SendMessage error", "session", sess.ID, "msgID", msgID, "err", err)
		markFailed(sess, msgID)
		return
	}

	var payloadDOWN PayloadDOWN
	payloadDOWN.Type = "text"
	
	// Check for tool calls first
	for _, cand := range resp.Candidates {
		if cand.Content != nil {
			for _, part := range cand.Content.Parts {
				if part.FunctionCall != nil {
					payloadDOWN.Type = "tool_call"
					payloadDOWN.ToolName = part.FunctionCall.Name
					payloadDOWN.ToolArgs = part.FunctionCall.Args
					break
				}
			}
		}
	}

	// If no tool call, extract text
	if payloadDOWN.Type == "text" {
		var out strings.Builder
		for _, cand := range resp.Candidates {
			if cand.Content != nil {
				for _, part := range cand.Content.Parts {
					if part.Text != "" {
						out.WriteString(part.Text)
					}
				}
			}
		}
		payloadDOWN.Content = out.String()
	}

	downBytes, _ := json.Marshal(payloadDOWN)
	responseBase64 := base64.StdEncoding.EncodeToString(downBytes)

	// Chunk response into 200 character chunks (fits in single TXT record string < 255 safely)
	var chunks []string
	chunkSize := 200
	for i := 0; i < len(responseBase64); i += chunkSize {
		end := i + chunkSize
		if end > len(responseBase64) {
			end = len(responseBase64)
		}
		chunks = append(chunks, responseBase64[i:end])
	}

	mu.Lock()
	sess.Responses[msgID].Chunks = chunks
	sess.Responses[msgID].Ready = true
	mu.Unlock()
	log.Info("Response ready", "session", sess.ID, "msgID", msgID, "chunks", len(chunks))
}

func markFailed(sess *Session, msgID int) {
	mu.Lock()
	if sess.Responses[msgID] == nil {
		sess.Responses[msgID] = &MessageResponse{}
	}
	sess.Responses[msgID].Failed = true
	sess.Responses[msgID].Ready = false
	mu.Unlock()
}

func (rw *dohResponseWriter) LocalAddr() net.Addr { return rw.localAddr }
func (rw *dohResponseWriter) RemoteAddr() net.Addr { return rw.remoteAddr }
func (rw *dohResponseWriter) WriteMsg(m *dns.Msg) error {
	rw.msg = m
	return nil
}
func (rw *dohResponseWriter) Write(b []byte) (int, error) { return 0, fmt.Errorf("not implemented") }
func (rw *dohResponseWriter) Close() error { return nil }
func (rw *dohResponseWriter) TsigStatus() error { return nil }
func (rw *dohResponseWriter) TsigTimersOnly(b bool) {}
func (rw *dohResponseWriter) Hijack() {}

type dohResponseWriter struct {
	localAddr  net.Addr
	remoteAddr net.Addr
	msg        *dns.Msg
}

func main() {
	godotenv.Load("../.env") // Assuming server is run from server/ or project root
	godotenv.Load(".env")

	log.SetReportTimestamp(true)

	if os.Getenv("GEMINI_API_KEY") == "" {
		log.Fatal("GEMINI_API_KEY environment variable is not set in .env or environment")
	}

	rand.Seed(time.Now().UnixNano())

	mux := dns.NewServeMux()
	mux.HandleFunc("llm.local.", handleQuery)

	useDoT := os.Getenv("USE_DOT") == "true"
	useDoH := os.Getenv("USE_DOH") == "true"
	port := 53535
	netType := "udp"
	
	if useDoT {
		port = 853
		netType = "tcp-tls"
	} else if useDoH {
		port = 443
		netType = "https"
	}
	
	if envPort := os.Getenv("SERVER_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		}
	}
	
	// Bind to 0.0.0.0 if using DoT/DoH to support Google Cloud / external access. Support local fallback if UDP.
	serverAddr := fmt.Sprintf("0.0.0.0:%d", port)
	if !(useDoT || useDoH) {
		serverAddr = fmt.Sprintf("127.0.0.1:%d", port)
	}

	var cert tls.Certificate
	if useDoT || useDoH {
		certPath := os.Getenv("TLS_CERT")
		keyPath := os.Getenv("TLS_KEY")
		if certPath == "" || keyPath == "" {
			certPath = "cert.pem"
			keyPath = "key.pem"
		}
		
		if _, err := os.Stat(certPath); os.IsNotExist(err) {
			if _, err2 := os.Stat("../" + certPath); err2 == nil {
				certPath = "../" + certPath
				keyPath = "../" + keyPath
			}
		}
		
		var tlsErr error
		cert, tlsErr = tls.LoadX509KeyPair(certPath, keyPath)
		if tlsErr != nil {
			log.Fatalf("Failed to load TLS certificates (%s, %s): %s", certPath, keyPath, tlsErr)
		}
	}

	log.Infof("Starting Agentic LLM DNS server on %s (Net: %s) ...", serverAddr, netType)

	if useDoH {
		http.HandleFunc("/dns-query", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost && r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			var msgData []byte
			var err error

			if r.Method == http.MethodPost {
				if r.Header.Get("Content-Type") != "application/dns-message" {
					http.Error(w, "Unsupported Media Type", http.StatusUnsupportedMediaType)
					return
				}
				msgData, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			} else if r.Method == http.MethodGet {
				dnsParam := r.URL.Query().Get("dns")
				if dnsParam == "" {
					http.Error(w, "Missing dns parameter", http.StatusBadRequest)
					return
				}
				msgData, err = base64.RawURLEncoding.DecodeString(dnsParam)
				if err != nil {
					msgData, err = base64.URLEncoding.DecodeString(dnsParam)
					if err != nil {
						http.Error(w, "Invalid dns parameter", http.StatusBadRequest)
						return
					}
				}
			}

			msg := new(dns.Msg)
			if err := msg.Unpack(msgData); err != nil {
				http.Error(w, "Bad Request", http.StatusBadRequest)
				return
			}

			var remoteAddr net.Addr
			if tcpAddr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr); err == nil {
				remoteAddr = tcpAddr
			} else {
				remoteAddr = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
			}

			localAddr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
			if !ok {
				localAddr = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 443}
			}

			rw := &dohResponseWriter{
				localAddr:  localAddr,
				remoteAddr: remoteAddr,
			}

			mux.ServeDNS(rw, msg)

			if rw.msg == nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			respBytes, err := rw.msg.Pack()
			if err != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/dns-message")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			w.Write(respBytes)
		})

		httpServer := &http.Server{
			Addr: serverAddr,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
			},
		}

		log.Info("Server successfully started! Ready for DoH queries.", "addr", serverAddr, "net", netType)
		err := httpServer.ListenAndServeTLS("", "")
		if err != nil {
			if strings.Contains(err.Error(), "permission denied") {
				log.Fatalf("Failed to start DoH server on port %d. This requires root! Did you run with sudo? Error: %v", port, err)
			}
			log.Fatalf("Failed to start server on %s: %s", serverAddr, err)
		}
	} else {
		server := &dns.Server{
			Addr:    serverAddr,
			Net:     netType,
			Handler: mux,
		}

		if useDoT {
			server.TLSConfig = &tls.Config{
				Certificates: []tls.Certificate{cert},
			}
		}

		server.NotifyStartedFunc = func() {
			log.Info("Server successfully started! Ready for queries.", "addr", serverAddr, "net", netType)
		}

		err := server.ListenAndServe()
		if err != nil {
			log.Fatalf("Failed to start server: %s", err)
		}
	}
}

