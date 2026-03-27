package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"github.com/miekg/dns"
	"golang.org/x/term"
)

var (
	serverAddr         = "127.0.0.1:53535"
	domain             = "llm.local."
	useDoT             = false
	useDoH             = false
	insecureSkipVerify = false
)

type PayloadUP struct {
	Type     string `json:"type"`
	Content  string `json:"content"`
	ToolName string `json:"tool_name,omitempty"`
}

type PayloadDOWN struct {
	Type     string         `json:"type"`
	Content  string         `json:"content"`
	ToolName string         `json:"tool_name,omitempty"`
	ToolArgs map[string]any `json:"tool_args,omitempty"`
}

var sessionID string
var msgID int

func main() {
	godotenv.Load("../.env")
	godotenv.Load(".env")

	if envAddr := os.Getenv("DNS_SERVER_ADDR"); envAddr != "" {
		serverAddr = envAddr
	}
	if os.Getenv("USE_DOT") == "true" {
		useDoT = true
		if os.Getenv("DNS_SERVER_ADDR") == "" {
			serverAddr = "127.0.0.1:853" // Default to DoT port
		}
	} else if os.Getenv("USE_DOH") == "true" {
		useDoH = true
		if os.Getenv("DNS_SERVER_ADDR") == "" {
			serverAddr = "https://127.0.0.1/dns-query" // Default DoH endpoint
		}
	}
	if os.Getenv("INSECURE_SKIP_VERIFY") == "true" {
		insecureSkipVerify = true
	}

	// Clean, unmistakable ASCII Art with Gradient
	banner := []string{
		`▓█████▄  ███▄    █   ██████     ██▓     ██▓     ███▄ ▄███▓`,
		`▒██▀ ██▌ ██ ▀█   █ ▒██    ▒    ▓██▒    ▓██▒    ▓██▒▀█▀ ██▒`,
		`░██   █▌▓██  ▀█ ██▒░ ▓██▄      ▒██░    ▒██░    ▓██    ▓██░`,
		`░▓█▄   ▌▓██▒  ▐▌██▒  ▒   ██▒   ▒██░    ▒██░    ▒██    ▒██ `,
		`░▒████▓ ▒██░   ▓██░▒██████▒▒   ░██████▒░██████▒▒██▒   ░██▒`,
		` ▒▒▓  ▒ ░ ▒░   ▒ ▒ ▒ ▒▓▒ ▒ ░   ░ ▒░▓  ░░ ▒░▓  ░░ ▒░   ░  ░`,
		` ░ ▒  ▒ ░ ░░   ░ ▒░░ ░▒  ░ ░   ░ ░ ▒  ░░ ░ ▒  ░░  ░      ░`,
		` ░ ░  ░    ░   ░ ░ ░  ░  ░       ░ ░     ░ ░   ░      ░   `,
		`   ░             ░       ░         ░  ░    ░  ░       ░   `,
		` ░                                                        `,
	}
	gradColors := []string{
		"#00FFFF", "#1CE2FF", "#38C6FF", "#55AAFF", "#718EFF",
		"#8E71FF", "#AA55FF", "#C638FF", "#E21CFF", "#FF00FF",
	}
	for i, line := range banner {
		fmt.Println(lipgloss.NewStyle().Foreground(lipgloss.Color(gradColors[i])).Render(line))
	}

	systemColor := color.New(color.FgHiBlack)
	systemColor.Println("          A stealthy, high-capacity Agentic DNS tunnel")
	systemColor.Println("          Powered by Gemini 3.1 Pro Preview")
	fmt.Println()
	systemColor.Println("──────────────────────────────────────────────────────────────────")

	initSession(true)

	systemColor.Println("Type your message below. Press Enter to send, or Ctrl+C to exit.")
	systemColor.Println("Type /help for a list of commands.")
	systemColor.Println("Ready for input.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)

	youStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#000000")).
		Background(lipgloss.Color("#00FFFF")).
		Padding(0, 1).
		MarginRight(1)

	promptCursor := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FFFF")).Render("❯")

	for {
		fmt.Print(youStyle.Render("You") + promptCursor + " ")
		
		if !scanner.Scan() {
			break
		}
		
		text := scanner.Text()
		cmdText := strings.TrimSpace(text)
		if cmdText == "" {
			continue
		}

		if cmdText == "/exit" || cmdText == "/quit" {
			color.HiBlack("\nExiting session. Goodbye!\n")
			break
		} else if cmdText == "/clear" || cmdText == "/new" || cmdText == "/reset" {
			fmt.Println()
			color.HiCyan("Starting a new session...\n")
			initSession(true)
			continue
		} else if cmdText == "/help" {
			printHelp()
			continue
		} else if strings.HasPrefix(cmdText, "/compact") {
			focus := strings.TrimSpace(strings.TrimPrefix(cmdText, "/compact"))
			if focus == "" {
				text = "SYSTEM COMMAND: Please summarize our conversation so far, keeping all important facts and context, then acknowledge you have compacted your memory to save space."
			} else {
				text = fmt.Sprintf("SYSTEM COMMAND: Please summarize our conversation so far, keeping all important context. Acknowledge you have compacted your memory. For all future responses, please focus strictly on: %s", focus)
			}
			color.HiMagenta("\n[Sending compact instruction to Agent...]\n")
		}

		payload := PayloadUP{
			Type:    "user",
			Content: text,
		}

		err := processMessageLoop(payload)
		if err != nil {
			color.Red("\n[Error: %v]\n", err)
		}
		fmt.Println()
	}
}

func initSession(showInitMsg bool) {
	s := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
	s.Suffix = " " + color.HiBlackString("Initializing Agent Session...")
	s.Color("magenta")
	s.Start()

	id, err := doDNSQuery("init." + domain)
	if err != nil {
		s.Stop()
		color.Red("Failed to initialize session: %v\n", err)
		os.Exit(1)
	}
	sessionID = strings.TrimSpace(id)
	msgID = 0
	
	s.Stop()
	if showInitMsg {
		color.New(color.FgHiBlack).Printf("Session ID established: %s\n", sessionID)
	}
}

func printHelp() {
	fmt.Println()
	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F39C12")).Bold(true)
	fmt.Println(helpStyle.Render("Available Commands:"))
	fmt.Println("  /help                     - Show this help message")
	fmt.Println("  /clear, /new, /reset      - Start a new chat session")
	fmt.Println("  /exit, /quit              - Exit the application")
	fmt.Println("  /compact [instructions]   - Ask the LLM to summarize and compact context")
	fmt.Println()
}

func doDNSQuery(qname string) (string, error) {
	m := new(dns.Msg)
	m.SetQuestion(qname, dns.TypeTXT)
	
	if useDoH {
		return doDoHQuery(m)
	}

	c := new(dns.Client)
	c.Net = "udp"
	
	if useDoT {
		c.Net = "tcp-tls"
		c.TLSConfig = &tls.Config{InsecureSkipVerify: insecureSkipVerify}
	}
	
	r, _, err := c.Exchange(m, serverAddr)
	if err != nil {
		return "", err
	}
	if r.Rcode != dns.RcodeSuccess {
		return "", fmt.Errorf("DNS query failed: %v", dns.RcodeToString[r.Rcode])
	}
	if len(r.Answer) == 0 {
		return "", fmt.Errorf("No TXT record found")
	}
	if txt, ok := r.Answer[0].(*dns.TXT); ok {
		return strings.Join(txt.Txt, ""), nil
	}
	return "", fmt.Errorf("Answer is not a TXT record")
}

func doDoHQuery(m *dns.Msg) (string, error) {
	msgData, err := m.Pack()
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, serverAddr, bytes.NewReader(msgData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureSkipVerify},
		},
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DoH request failed with status: %d", resp.StatusCode)
	}

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	r := new(dns.Msg)
	if err := r.Unpack(respData); err != nil {
		return "", err
	}

	if r.Rcode != dns.RcodeSuccess {
		return "", fmt.Errorf("DNS query failed: %v", dns.RcodeToString[r.Rcode])
	}
	if len(r.Answer) == 0 {
		return "", fmt.Errorf("No TXT record found")
	}
	if txt, ok := r.Answer[0].(*dns.TXT); ok {
		return strings.Join(txt.Txt, ""), nil
	}
	return "", fmt.Errorf("Answer is not a TXT record")
}

func processMessageLoop(initialPayload PayloadUP) error {
	nextPayload := initialPayload

	for {
		msgID++
		currentMsgID := msgID

		s := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
		s.Suffix = " " + color.HiBlackString(fmt.Sprintf("Transmitting payload [%d] over DNS...", currentMsgID))
		s.Color("magenta")
		s.Start()

		payloadBytes, err := json.Marshal(nextPayload)
		if err != nil {
			s.Stop()
			return fmt.Errorf("failed to encode JSON: %v", err)
		}

		// 1. Send chunks
		chunkSize := 35 
		encoder := base32.StdEncoding.WithPadding(base32.NoPadding)
		
		seq := 0
		for i := 0; i < len(payloadBytes); i += chunkSize {
			end := i + chunkSize
			if end > len(payloadBytes) {
				end = len(payloadBytes)
			}
			
			chunkBase32 := encoder.EncodeToString(payloadBytes[i:end])
			// Format: <chunk>.<seq>.up.<msgID>.<sessionID>.llm.local.
			qname := fmt.Sprintf("%s.%d.up.%d.%s.%s", strings.ToLower(chunkBase32), seq, currentMsgID, sessionID, domain)
			
			resp, err := doDNSQuery(qname)
			if err != nil {
				s.Stop()
				return fmt.Errorf("failed to send chunk %d: %v", seq, err)
			}
			if resp != "ACK" {
				s.Stop()
				return fmt.Errorf("server did not ACK chunk %d, got: %s", seq, resp)
			}
			seq++
		}

		// 2. Send fin
		s.Suffix = " " + color.HiBlackString(fmt.Sprintf("Waiting for Agent [%d]...", currentMsgID))
		finQname := fmt.Sprintf("fin.%d.%s.%s", currentMsgID, sessionID, domain)
		resp, err := doDNSQuery(finQname)
		if err != nil {
			s.Stop()
			return fmt.Errorf("failed to send fin: %v", err)
		}
		if resp != "ACK" {
			s.Stop()
			return fmt.Errorf("server did not ACK fin, got: %s", resp)
		}

		// 3. Poll and receive response
		downSeq := 0
		var fullResponseBuilder strings.Builder

		for {
			// Format: <seq>.<msgID>.down.<id>.llm.local.
			qname := fmt.Sprintf("%d.%d.down.%s.%s", downSeq, currentMsgID, sessionID, domain)
			resp, err := doDNSQuery(qname)
			if err != nil {
				s.Stop()
				return fmt.Errorf("failed to poll chunk %d: %v", downSeq, err)
			}

			if resp == "PENDING" {
				time.Sleep(500 * time.Millisecond)
				continue
			} else if resp == "EOF" {
				break
			} else if strings.HasPrefix(resp, "ERR:") {
				s.Stop()
				return fmt.Errorf("server error: %s", resp)
			}

			// Decode Base64 chunk
			decoded, err := base64.StdEncoding.DecodeString(resp)
			if err != nil {
				return fmt.Errorf("failed to base64 decode chunk: %v\nChunk was: %s", err, resp)
			}
			
			s.Suffix = " " + color.HiBlackString(fmt.Sprintf("Receiving chunk %d...", downSeq))
			fullResponseBuilder.Write(decoded)
			downSeq++
		}

		s.Stop()

		var payloadDOWN PayloadDOWN
		err = json.Unmarshal([]byte(fullResponseBuilder.String()), &payloadDOWN)
		if err != nil {
			return fmt.Errorf("failed to decode JSON response: %v\nResponse was: %s", err, fullResponseBuilder.String())
		}

		if payloadDOWN.Type == "tool_call" {
			toolTitleStyle := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#000000")).
				Background(lipgloss.Color("#F39C12")).
				Padding(0, 1).
				MarginTop(1).
				MarginBottom(1)
			
			fmt.Println(toolTitleStyle.Render("Agent Tool Executing"))
			
			toolResult := ""

			if payloadDOWN.ToolName == "client_execute_bash" {
				cmdStr, ok := payloadDOWN.ToolArgs["command"].(string)
				if !ok {
					toolResult = "Error: Invalid command argument"
				} else {
					cmdStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F39C12")).Bold(true)
					fmt.Println(cmdStyle.Render("⚙️  Targeting Bash Command:"))
					fmt.Println("  " + color.HiBlackString(cmdStr))
					fmt.Println()
					fmt.Print(color.HiCyanString("  Allow this command to run? [Y/n]: "))
					
					reader := bufio.NewReader(os.Stdin)
					confirm, _ := reader.ReadString('\n')
					confirm = strings.TrimSpace(strings.ToLower(confirm))
					
					if confirm == "y" || confirm == "yes" || confirm == "" {
						out, cmdErr := exec.Command("bash", "-c", cmdStr).CombinedOutput()
						if cmdErr != nil {
							toolResult = fmt.Sprintf("Execution Error: %v\nOutput: %s", cmdErr, string(out))
						} else {
							toolResult = string(out)
							if strings.TrimSpace(toolResult) == "" {
								toolResult = "Success (No Output)"
							}
						}
					} else {
						color.HiRed("\n  Command execution rejected by user.")
						toolResult = "User rejected the command execution. Ask them what they would like to do instead or try another approach."
					}
					if len(toolResult) > 1500 {
						toolResult = toolResult[:1500] + "\n...[Output Truncated]"
					}
				}
			} else if payloadDOWN.ToolName == "client_read_file" {
				filePath, ok := payloadDOWN.ToolArgs["filepath"].(string)
				if !ok {
					toolResult = "Error: Invalid filepath argument"
				} else {
					readingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#3498DB")).Bold(true)
					fmt.Println(readingStyle.Render("📄 Reading File:") + " " + color.HiWhiteString(filePath))
					
					out, fileErr := os.ReadFile(filePath)
					if fileErr != nil {
						toolResult = fmt.Sprintf("Error reading file: %v", fileErr)
					} else {
						toolResult = string(out)
						if len(toolResult) > 1500 {
							toolResult = toolResult[:1500] + "\n...[Content Truncated]"
						}
					}
				}
			} else {
				toolResult = "Error: Unknown tool name requested by server."
			}

			w, _, _ := term.GetSize(int(os.Stdout.Fd()))
			if w == 0 {
				w = 80
			}
			boxStyle := lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#F39C12")).
				Padding(0, 1).
				MaxWidth(w - 4)

			fmt.Println("\n" + boxStyle.Render(strings.TrimSpace(toolResult)) + "\n")

			// Automatically formulate the response and continue the loop
			nextPayload = PayloadUP{
				Type:     "tool_response",
				Content:  toolResult,
				ToolName: payloadDOWN.ToolName,
			}
			continue
		} else {
			// Normal Text Response
			geminiStyle := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(lipgloss.Color("#a220b3")).
				Padding(0, 1).
				MarginTop(1)
			
			fmt.Println(geminiStyle.Render("Gemini"))
			
			width, _, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil || width < 40 {
				width = 80
			}
			
			wrapWidth := width - 4
			if wrapWidth < 40 {
				wrapWidth = 40
			}
			if wrapWidth > 120 {
				wrapWidth = 120
			}
			
			// Use glamour to render markdown
			r, _ := glamour.NewTermRenderer(
				glamour.WithEnvironmentConfig(),
				glamour.WithWordWrap(wrapWidth),
			)
			
			out, err := r.Render(payloadDOWN.Content)
			if err != nil {
				fmt.Println(payloadDOWN.Content)
			} else {
				fmt.Println(strings.TrimRight(out, "\n"))
			}

			// Break out of the loop and wait for next user input
			break
		}
	}

	return nil
}

