package engine

import (
	"context"
	"sync"
	"time"

	"localaw/internal/command"
	"localaw/internal/llm"
	"localaw/internal/local"
)

// Phase is the conversation phase of a session.
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseLLM      Phase = "llm"
	PhaseExec     Phase = "exec"
	PhaseAuthWait Phase = "auth_wait"
)

// PendingCommand is an action awaiting user authorization.
type PendingCommand struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
	Source string         `json:"source,omitempty"` // builtin | mcp
	Server string         `json:"server,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	Title  string         `json:"title,omitempty"`
}

// TranscriptEntry is one displayable line of a session's conversation.
type TranscriptEntry struct {
	Role string `json:"role"` // user | assistant | result | system
	Text string `json:"text"`
}

// Session is one independent conversation.
type Session struct {
	ID        string
	Title     string
	CreatedAt time.Time

	mu           sync.Mutex
	history      []llm.Message
	transcript   []TranscriptEntry
	phase        Phase
	text         string
	seq          uint64
	pending      *PendingCommand
	stop         bool
	running      bool
	resetPending bool
	authCh       chan bool
	job          *local.Job
	runCtx       context.Context
	cancel       context.CancelFunc
	done         chan struct{}
}

func (s *Session) addTranscript(role, text string) uint64 {
	s.mu.Lock()
	s.transcript = append(s.transcript, TranscriptEntry{Role: role, Text: text})
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	return seq
}

func (s *Session) transcriptCopy() []TranscriptEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TranscriptEntry, len(s.transcript))
	copy(out, s.transcript)
	return out
}

// Snapshot is a copy of session state for UI replay.
type Snapshot struct {
	Text       string            `json:"text"`
	Seq        uint64            `json:"seq"`
	Pending    *PendingCommand   `json:"pending,omitempty"`
	Running    bool              `json:"running"`
	Transcript []TranscriptEntry `json:"transcript"`
}

// Snapshot returns a consistent copy of the session state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	tr := make([]TranscriptEntry, len(s.transcript))
	copy(tr, s.transcript)
	var pending *PendingCommand
	if s.pending != nil {
		cp := *s.pending
		pending = &cp
	}
	return Snapshot{
		Text:       s.text,
		Seq:        s.seq,
		Pending:    pending,
		Running:    s.running,
		Transcript: tr,
	}
}

// publishNow assigns the next event sequence and broadcasts ev.
func (e *Engine) publishNow(s *Session, ev Event) {
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	ev["seq"] = seq
	e.hub.publish(s.ID, ev)
}

// publish broadcasts ev carrying a sequence already assigned under the session
// lock (used when the state mutation and the seq increment are atomic).
func (e *Engine) publish(s *Session, seq uint64, ev Event) {
	ev["seq"] = seq
	e.hub.publish(s.ID, ev)
}

func (s *Session) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stop
}

// Phase returns the current conversation phase (cheap status accessor).
func (s *Session) Phase() Phase {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

// Running reports whether a conversation is currently in progress.
func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Session) historyCopy() []llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.Message, len(s.history))
	copy(out, s.history)
	return out
}

// persistPartial moves any in-progress assistant text into the transcript.
func (e *Engine) persistPartial(s *Session) {
	s.mu.Lock()
	partial := s.text
	s.text = ""
	s.mu.Unlock()
	if partial != "" {
		s.addTranscript("assistant", partial)
	}
}

// markStopped records a stop in the transcript and broadcasts the stopped event.
func (e *Engine) markStopped(s *Session) {
	e.persistPartial(s)
	s.addTranscript("system", "[Stopped by user]")
	e.publishNow(s, Event{"type": EventStopped})
}

// runConversation executes the tool-calling loop for one user message. The user
// message itself is persisted by BeginChat before this goroutine starts.
func (e *Engine) runConversation(s *Session) {
	defer e.finish(s)

	system := e.RenderSystemPrompt()
	round := 0

	for round < e.cfg.Policy.RoundLimit {
		if s.isStopped() {
			e.markStopped(s)
			return
		}
		round++
		s.mu.Lock()
		s.text = ""
		s.pending = nil
		s.phase = PhaseLLM
		ctx := s.runCtx
		s.mu.Unlock()

		e.publishNow(s, Event{"type": EventAnswering})

		msgs := e.buildMessages(s, system)
		res, err := e.llm.Chat(ctx, msgs, e.reg.OpenAITools(), func(chunk string) {
			s.mu.Lock()
			s.text += chunk
			s.seq++
			seq := s.seq
			s.mu.Unlock()
			e.publish(s, seq, Event{"type": EventChunk, "content": chunk})
		})
		if err != nil {
			if s.isStopped() {
				e.markStopped(s)
				return
			}
			e.persistPartial(s)
			seq := s.addTranscript("system", "[LLM Error] "+err.Error())
			e.publish(s, seq, Event{"type": EventLLMError, "error": err.Error()})
			return
		}

		s.mu.Lock()
		s.history = append(s.history, res.Raw)
		s.text = ""
		s.mu.Unlock()
		if res.Content != "" {
			s.addTranscript("assistant", res.Content)
		}

		if len(res.ToolCalls) == 0 {
			break
		}
		e.publishNow(s, Event{"type": EventResponseDone})

		denied := false
		for i, tc := range res.ToolCalls {
			if s.isStopped() {
				for _, rest := range res.ToolCalls[i:] {
					e.appendToolRaw(s, rest.ID, "Error: aborted before execution")
				}
				e.markStopped(s)
				return
			}
			action := tc.Name
			params, perr := parseArgs(tc.Arguments)
			if perr != nil {
				msg := "Error: invalid arguments: " + perr.Error()
				seq := e.appendTool(s, tc.ID, action, params, msg)
				e.publish(s, seq, e.toolEvent(action, Event{"type": EventExecutionDone, "action": action, "params": params, "result": msg}))
				continue
			}
			def, known := e.reg.Get(action)
			if !known {
				msg := "Error: unknown action: " + action
				seq := e.appendTool(s, tc.ID, action, params, msg)
				e.publish(s, seq, e.toolEvent(action, Event{"type": EventExecutionDone, "action": action, "params": params, "result": msg}))
				continue
			}
			if !command.CheckSafety(action, params) {
				msg := "Error: blocked by safety check"
				seq := e.appendTool(s, tc.ID, action, params, msg)
				e.publish(s, seq, e.toolEvent(action, Event{"type": EventExecutionDone, "action": action, "params": params, "result": msg}))
				continue
			}

			if e.RequiresAuth(action) {
				authCh := make(chan bool, 1)
				s.mu.Lock()
				s.pending = e.pendingFor(action, params)
				s.phase = PhaseAuthWait
				s.authCh = authCh
				s.mu.Unlock()
				e.publishNow(s, e.toolEvent(action, Event{"type": EventAuthRequired, "action": action, "params": params}))
				ok := e.awaitAuth(s, authCh)
				s.mu.Lock()
				s.pending = nil
				if s.authCh == authCh {
					s.authCh = nil
				}
				s.mu.Unlock()
				if s.isStopped() {
					for _, rest := range res.ToolCalls[i:] {
						e.appendToolRaw(s, rest.ID, "Error: aborted before execution")
					}
					e.markStopped(s)
					return
				}
				if !ok {
					const deniedMsg = "Error: user denied command execution (not executed)"
					e.appendToolRaw(s, tc.ID, deniedMsg)
					seq := s.addTranscript("system", "Denied: "+e.displayName(action)+" (not executed)")
					for _, rest := range res.ToolCalls[i+1:] {
						e.appendToolRaw(s, rest.ID, "Error: skipped because a previous command was denied")
					}
					e.publish(s, seq, e.toolEvent(action, Event{"type": EventExecutionDenied, "action": action, "params": params}))
					denied = true
					break
				}
				round = 0
			}

			s.mu.Lock()
			s.phase = PhaseExec
			s.mu.Unlock()
			e.publishNow(s, e.toolEvent(action, Event{"type": EventExecuting, "action": action, "params": params}))

			job := local.NewJob(ctx, action, params, s.ID)
			job.Timeout = time.Duration(def.Timeout) * time.Second
			s.mu.Lock()
			s.job = job
			s.mu.Unlock()
			out, jerr := e.queue.Run(job)
			s.mu.Lock()
			s.job = nil
			s.mu.Unlock()

			if jerr != nil {
				if jerr == local.ErrCancelled {
					// Answer this and any remaining tool calls so the assistant
					// message never dangles without matching tool results.
					for _, rest := range res.ToolCalls[i:] {
						e.appendToolRaw(s, rest.ID, "Error: aborted before execution")
					}
					e.markStopped(s)
					return
				}
				// Any other failure (timeout, queue closed) is terminal for this
				// turn: persist the error, answer the remaining tool calls and end.
				seq := e.appendTool(s, tc.ID, action, params, "Error: "+jerr.Error())
				for _, rest := range res.ToolCalls[i+1:] {
					e.appendToolRaw(s, rest.ID, "Error: skipped because a previous command failed")
				}
				e.publish(s, seq, e.toolEvent(action, Event{"type": EventExecutionError, "action": action, "params": params, "error": jerr.Error()}))
				return
			}
			seq := e.appendTool(s, tc.ID, action, params, out)
			e.publish(s, seq, e.toolEvent(action, Event{"type": EventExecutionDone, "action": action, "params": params, "result": out}))
		}
		if denied {
			break
		}
	}
}

// finish marks a session idle and emits exactly one terminal "done" event. If a
// reset was scheduled while a non-cancellable task was still running, it is
// applied here and a "reset" event is broadcast so attached clients clear.
func (e *Engine) finish(s *Session) {
	s.mu.Lock()
	running := s.running
	s.running = false
	s.phase = PhaseIdle
	s.stop = false
	s.pending = nil
	s.job = nil
	runDone := s.done
	s.done = nil
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	pendingReset := s.resetPending
	if pendingReset {
		s.history = nil
		s.transcript = nil
		s.text = ""
		s.resetPending = false
	}
	s.mu.Unlock()
	if running {
		e.publishNow(s, Event{"type": EventDone})
	}
	if pendingReset {
		e.publishNow(s, Event{"type": EventReset})
	}
	if runDone != nil {
		close(runDone)
	}
}

func (e *Engine) buildMessages(s *Session, system string) []llm.Message {
	hist := s.historyCopy()
	msgs := make([]llm.Message, 0, len(hist)+1)
	msgs = append(msgs, llm.SystemMessage(system))
	msgs = append(msgs, hist...)
	return msgs
}

func (e *Engine) appendTool(s *Session, id, action string, params map[string]any, content string) uint64 {
	if content == "" {
		content = "(no output)"
	}
	label := e.displayName(action)
	text := "[" + label + "]" + actionDetail(action, params) + "\n" + content
	s.mu.Lock()
	s.history = append(s.history, llm.ToolMessage(content, id))
	s.transcript = append(s.transcript, TranscriptEntry{Role: "result", Text: text})
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	return seq
}

// appendToolRaw appends only the tool message to the LLM history (used for
// denials / skips).
func (e *Engine) appendToolRaw(s *Session, id, content string) uint64 {
	s.mu.Lock()
	s.history = append(s.history, llm.ToolMessage(content, id))
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	return seq
}
