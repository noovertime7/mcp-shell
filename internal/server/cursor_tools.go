package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/noovertime7/mcp-shell/internal/cursorctl"
)

type cursorReadIn struct {
	Agent string `json:"agent" jsonschema:"agent index or a keyword matching exactly one agent, from cursor_agents"`
	Tail  int    `json:"tail,omitempty" jsonschema:"characters from the end of the conversation (default 4000, max 50000)"`
}

type cursorSendIn struct {
	Agent string `json:"agent" jsonschema:"agent index or a keyword matching exactly one agent; indexes shift as agents get activity, so list again right before sending"`
	Text  string `json:"text" jsonschema:"message to send"`
	Mode  string `json:"mode,omitempty" jsonschema:"how to send to a running agent: queue waits until its current work finishes, now delivers right away (same as Ctrl+Enter in Cursor); required when the agent is busy"`
}

type cursorNewIn struct {
	Text string `json:"text" jsonschema:"first message for the new agent; put a unique tag in the first line, since it usually becomes the agent's title and lets you find it again"`
}

func formatCursorAgents(agents []cursorctl.Agent) string {
	if len(agents) == 0 {
		return "no agents\n"
	}
	var b strings.Builder
	for _, a := range agents {
		fmt.Fprintf(&b, "%s\n", formatCursorAgent(a))
	}
	return b.String()
}

func formatCursorAgent(a cursorctl.Agent) string {
	state, mark := "idle", " "
	if a.Busy {
		state = "busy"
	}
	if a.Selected {
		mark = "*"
	}
	line := fmt.Sprintf("%d %s %s %s", a.Index, mark, state, a.Title)
	if a.Sub != "" {
		line += " | " + a.Sub
	}
	return line
}

func (s *Server) registerCursorTools() {
	c := cursorctl.New()
	addTool(s, "cursor_agents", "List the agents in the running Cursor on this host, one per line: index, * if it is on screen, busy or idle, title and latest activity. idle means the agent has stopped and is waiting for a message. Indexes shift as agents get activity.",
		func(ctx context.Context, _ struct{}) (string, error) {
			agents, err := c.Agents(ctx)
			if err != nil {
				return "", err
			}
			return formatCursorAgents(agents), nil
		})
	addTool(s, "cursor_read", "Read the end of one Cursor agent's conversation as shown on screen. Briefly switches Cursor to that agent, then back.",
		func(ctx context.Context, in cursorReadIn) (string, error) {
			tail := in.Tail
			if tail <= 0 {
				tail = 4000
			}
			if tail > 50000 {
				tail = 50000
			}
			a, err := c.Read(ctx, in.Agent, tail)
			if err != nil {
				return "", err
			}
			return formatCursorAgent(a) + "\n--- conversation ---\n" + a.Text, nil
		})
	addTool(s, "cursor_send", "Send a message to one agent in the running Cursor, as if typed by the user. If the agent is running, mode decides: queue sends after its current work finishes, now delivers right away and it then carries on (a running command may be restarted); without mode it refuses. Also refuses if the input already holds text. Check with cursor_agents first: this acts on the user's real conversations.",
		func(ctx context.Context, in cursorSendIn) (string, error) {
			a, err := c.Send(ctx, in.Agent, in.Text, in.Mode)
			if err != nil {
				return "", err
			}
			return "sent to: " + formatCursorAgent(a), nil
		})
	addTool(s, "cursor_new", "Start a new agent in the running Cursor with a first message, using Cursor's current model and mode. Returns the agent list after sending; follow it with cursor_agents and cursor_read.",
		func(ctx context.Context, in cursorNewIn) (string, error) {
			agents, err := c.NewAgent(ctx, in.Text)
			if err != nil {
				return "", err
			}
			return "started\n" + formatCursorAgents(agents), nil
		})
}
