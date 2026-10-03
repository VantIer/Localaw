// Package cli implements the interactive terminal control loop.
package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"localaw/internal/config"
	"localaw/internal/engine"
)

// CLI is the interactive command loop.
type CLI struct {
	cfg    *config.Config
	eng    *engine.Engine
	reader *bufio.Reader
}

// Run starts the interactive loop.
func Run(cfg *config.Config, eng *engine.Engine) {
	c := &CLI{cfg: cfg, eng: eng, reader: bufio.NewReader(os.Stdin)}
	c.banner()
	for {
		prompt := "local> "
		if s := eng.ActiveSession(); s != nil {
			prompt = fmt.Sprintf("[%s]> ", s.Title)
		}
		fmt.Print(prompt)
		line, err := c.reader.ReadString('\n')
		if err != nil {
			fmt.Println()
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "/") {
			c.chat(line)
			continue
		}
		if c.command(line) {
			return
		}
	}
}

func (c *CLI) banner() {
	fmt.Println("============================================================")
	fmt.Println(" Localaw - local AI assistant")
	fmt.Println("============================================================")
	fmt.Printf("model: %s\n", c.cfg.LLM.Model)
	fmt.Printf("auth mode: %d   round limit: %d   cmd timeout: %ds\n",
		c.eng.AuthMode(), c.cfg.Policy.RoundLimit, c.cfg.Policy.CmdTimeout)
	fmt.Println("type /help for commands")
}

func (c *CLI) command(line string) bool {
	parts := strings.Fields(line)
	switch parts[0] {
	case "/quit", "/q":
		return true
	case "/help":
		fmt.Println(helpText)
	case "/sessions":
		c.listSessions()
	case "/new":
		title := ""
		if len(parts) > 1 {
			title = strings.Join(parts[1:], " ")
		}
		s, err := c.eng.NewSession(title)
		if err != nil {
			fmt.Println("error:", err)
			break
		}
		c.eng.SetActiveSession(s.ID)
		fmt.Println("created session", s.ID)
	case "/use":
		c.useSession(parts)
	case "/close":
		if len(parts) < 2 {
			fmt.Println("usage: /close <session_id>")
			break
		}
		if c.eng.CloseSession(parts[1]) {
			fmt.Println("closed", parts[1])
		} else {
			fmt.Println("closed", parts[1], "(a background task may still be finishing)")
		}
	case "/auth":
		c.authCmd(parts)
	case "/reset":
		s := c.eng.ActiveSession()
		if s == nil {
			fmt.Println("no active session")
			break
		}
		if c.eng.ResetConversation(s.ID) {
			fmt.Println("session reset")
		} else {
			fmt.Println("reset scheduled (will apply when the current task ends)")
		}
	case "/exec":
		// Take the command verbatim after "/exec " so quoting and repeated
		// whitespace are preserved.
		cmd := strings.TrimSpace(strings.TrimPrefix(line, "/exec"))
		if cmd == "" {
			fmt.Println("usage: /exec <command>")
			break
		}
		out, err := c.eng.RunDirect(cmd)
		c.printResult(out, err)
	default:
		fmt.Println("unknown command:", parts[0], "(try /help)")
	}
	return false
}

func (c *CLI) listSessions() {
	activeID := c.eng.ActiveSessionID()
	sessions := c.eng.ListSessions()
	if len(sessions) == 0 {
		fmt.Println("no sessions; /new to create one")
		return
	}
	for i, s := range sessions {
		mark := " "
		if s.ID == activeID {
			mark = "*"
		}
		status := "idle"
		if s.Running() {
			status = "running"
		}
		fmt.Printf(" %s [%d] %-22s %-8s %s\n", mark, i, s.ID, status, s.Title)
	}
}

func (c *CLI) useSession(parts []string) {
	if len(parts) < 2 {
		fmt.Println("usage: /use <session_id|index>")
		return
	}
	sessions := c.eng.ListSessions()
	arg := parts[1]
	if idx, err := strconv.Atoi(arg); err == nil && idx >= 0 && idx < len(sessions) {
		c.eng.SetActiveSession(sessions[idx].ID)
		fmt.Println("active session ->", sessions[idx].ID)
		return
	}
	if c.eng.SetActiveSession(arg) {
		fmt.Println("active session ->", arg)
		return
	}
	fmt.Println("session not found:", arg)
}

func (c *CLI) authCmd(parts []string) {
	if len(parts) == 1 {
		fmt.Println("auth mode:", c.eng.AuthMode())
		return
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 2 {
		fmt.Println("usage: /auth [0|1|2]")
		return
	}
	c.eng.SetAuthMode(m)
	fmt.Println("auth mode ->", m)
}

func (c *CLI) printResult(out string, err error) {
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(out)
}

func (c *CLI) chat(message string) {
	s := c.eng.ActiveSession()
	if s == nil {
		var err error
		if s, err = c.eng.EnsureSession(); err != nil {
			fmt.Println("error:", err)
			return
		}
	}
	ch, dropped, unsub := c.eng.Subscribe(s.ID)
	defer unsub()
	if err := c.eng.BeginChat(s.ID, message); err != nil {
		fmt.Println("error:", err)
		return
	}
	inStream := false
	for {
		var ev engine.Event
		select {
		case <-dropped:
			c.endStream(&inStream)
			fmt.Println("\n[stream dropped]")
			return
		case ev = <-ch:
		}
		switch ev["type"] {
		case "chunk":
			fmt.Print(ev["content"])
			inStream = true
		case "executing":
			c.endStream(&inStream)
			fmt.Printf("[executing...] %v %v\n", ev["action"], ev["params"])
		case "execution_done":
			c.endStream(&inStream)
			fmt.Printf("[result]\n%v\n", ev["result"])
		case "execution_error":
			c.endStream(&inStream)
			fmt.Printf("[result]\nError: %v\n", ev["error"])
		case "auth_required":
			c.endStream(&inStream)
			c.promptAuth(s.ID, ev)
		case "execution_denied":
			fmt.Println("[denied]")
		case "llm_error":
			c.endStream(&inStream)
			fmt.Println("\n[LLM Error]", ev["error"])
		case "error":
			c.endStream(&inStream)
			fmt.Println("\n[error]", ev["error"])
		case "stopped":
			c.endStream(&inStream)
			fmt.Println("\n[stopped]")
		case "done":
			c.endStream(&inStream)
			return
		}
	}
}

func (c *CLI) endStream(inStream *bool) {
	if *inStream {
		fmt.Println()
		*inStream = false
	}
}

func (c *CLI) promptAuth(sessionID string, ev engine.Event) {
	action, _ := ev["action"].(string)
	fmt.Printf("\n[auth] action=%v params=%v\n", ev["action"], ev["params"])
	for {
		fmt.Print("allow? /y /n /auth 0|1|2: ")
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return
		}
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "/y":
			c.eng.SubmitAuth(sessionID, true)
			return
		case "/n":
			c.eng.SubmitAuth(sessionID, false)
			return
		default:
			parts := strings.Fields(strings.TrimSpace(line))
			if len(parts) == 2 && parts[0] == "/auth" {
				if m, err := strconv.Atoi(parts[1]); err == nil && m >= 0 && m <= 2 {
					c.eng.SetAuthMode(m)
					fmt.Println("auth mode ->", m)
					if !c.eng.RequiresAuth(action) {
						fmt.Println("(no authorization required under the new mode; continuing)")
						c.eng.SubmitAuth(sessionID, true)
						return
					}
					continue
				}
			}
			fmt.Println("enter /y, /n or /auth <0|1|2>")
		}
	}
}

const helpText = `
/sessions                  list sessions
/new [title]               create a session
/use <session_id|index>    switch active session
/close <session_id>        close a session
/auth [0|1|2]              show / set authorization mode
/reset                     reset the active session
/exec <command>            run a shell command (direct)
/help                      this help
/quit                      exit
(any other input is sent to the LLM in the active session)`
