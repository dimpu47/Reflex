package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"jev-proxy/evaluator"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1).
			MarginBottom(1)

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	sreStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575"))

	llmStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00D8FF"))

	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF3366"))

	sysStyle = lipgloss.NewStyle().
			Faint(true).
			Foreground(lipgloss.Color("#888888"))
)

type model struct {
	viewport    viewport.Model
	messages    []string
	textarea    textarea.Model
	activeModel int
	models      []string
}

func initialModel() model {
	ta := textarea.New()
	ta.Placeholder = "Ask Reflex to rewrite the Wasm runbook..."
	ta.Focus()
	ta.Prompt = "┃ "
	ta.CharLimit = 1000
	ta.SetWidth(80)
	ta.SetHeight(3)

	vp := viewport.New(76, 18) // slightly smaller to account for borders
	initialText := sysStyle.Render("Welcome to Reflex Chaos Dojo\nPress [Tab] to switch models. Press [Ctrl+C] to exit.\n-------------------------------------------------------------")
	wrappedText := lipgloss.NewStyle().Width(vp.Width).Render(initialText)
	vp.SetContent(wrappedText)

	return model{
		textarea:    ta,
		messages:    []string{initialText},
		viewport:    vp,
		activeModel: 0,
		models:      []string{"Jev-Cloud", "Laya-Decision", "Local-Ollama (Qwen-Coder)"},
	}
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

type llmResponseMsg struct {
	text string
	err  error
}

func queryLLM(modelName, prompt string) tea.Cmd {
	return func() tea.Msg {
		if modelName == "Local-Ollama (Qwen-Coder)" {
			// Real conversational LLM via Ollama
			reqBody, _ := json.Marshal(map[string]interface{}{
				"model":  "qwen2.5-coder:7b",
				"prompt": prompt,
				"stream": false,
			})

			ollamaHost := os.Getenv("OLLAMA_HOST")
			if ollamaHost == "" {
				ollamaHost = "http://localhost:11434"
			}
			resp, err := http.Post(ollamaHost+"/api/generate", "application/json", bytes.NewBuffer(reqBody))
			if err != nil {
				return llmResponseMsg{err: fmt.Errorf("Ollama connection failed: %v", err)}
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			var result map[string]interface{}
			if err := json.Unmarshal(body, &result); err != nil {
				return llmResponseMsg{err: fmt.Errorf("Failed to parse response: %v", err)}
			}

			responseStr, ok := result["response"].(string)
			if !ok {
				if errMsg, ok := result["error"].(string); ok {
					return llmResponseMsg{err: fmt.Errorf("Ollama API Error: %s", errMsg)}
				}
				return llmResponseMsg{err: fmt.Errorf("Ollama API Error: %s", string(body))}
			}

			return llmResponseMsg{text: responseStr}
		}

		// Otherwise, it's a Decision API (Jev or Laya)
		endpoint := "https://api.jev.com/v1/evaluate" // Mock default for Jev
		if modelName == "Laya-Decision" {
			endpoint = "http://localhost:8081/v1/evaluate" // Laya runs locally
		}

		client := evaluator.NewRealClient("", endpoint)
		req := evaluator.EvaluationRequest{
			State:     []byte(fmt.Sprintf(`{"user_prompt": %q}`, prompt)),
			Questions: []string{"domain", "severity", "auto_recoverable"},
		}

		resp, err := client.Evaluate(context.Background(), req)
		if err != nil {
			return llmResponseMsg{err: fmt.Errorf("Evaluator API failed (%s): %v", endpoint, err)}
		}

		prettyResp := fmt.Sprintf("Domain: %s (%.0f%% confidence)\nSeverity: %.2f\nAuto-Recoverable: %.0f%%\nTokens Used: %d",
			resp.Answers.Domain.Choice,
			resp.Answers.Domain.Confidence*100,
			resp.Answers.Severity.Score,
			resp.Answers.AutoRecoverable.Probability*100,
			resp.Usage.InputTokens+resp.Usage.OutputTokens,
		)
		
		return llmResponseMsg{text: prettyResp}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyTab: // Model switcher
			m.activeModel = (m.activeModel + 1) % len(m.models)
			currentModel := m.models[m.activeModel]
			m.messages = append(m.messages, sysStyle.Render("System: Switched AI Provider to "+currentModel))
			wrappedText := lipgloss.NewStyle().Width(m.viewport.Width).Render(strings.Join(m.messages, "\n"))
			m.viewport.SetContent(wrappedText)
			m.viewport.GotoBottom()
			return m, nil
		case tea.KeyEnter:
			v := m.textarea.Value()
			if strings.TrimSpace(v) == "" {
				return m, nil
			}
			m.messages = append(m.messages, sreStyle.Render("SRE:")+" "+v)

			// Show loading indicator
			currentModel := m.models[m.activeModel]
			m.messages = append(m.messages, "\n"+llmStyle.Render("[LLM ("+currentModel+")]:")+" "+sysStyle.Render("Thinking..."))
			wrappedText := lipgloss.NewStyle().Width(m.viewport.Width).Render(strings.Join(m.messages, "\n"))
			m.viewport.SetContent(wrappedText)
			m.textarea.Reset()
			m.viewport.GotoBottom()

			return m, tea.Batch(tiCmd, vpCmd, queryLLM(currentModel, v))
		}
	case llmResponseMsg:
		// Remove the "Thinking..." message
		m.messages = m.messages[:len(m.messages)-1]

		if msg.err != nil {
			currentModel := m.models[m.activeModel]
			m.messages = append(m.messages, "\n"+errorStyle.Render("[LLM ("+currentModel+") Error]:")+" "+msg.err.Error())
		} else {
			currentModel := m.models[m.activeModel]
			m.messages = append(m.messages, "\n"+llmStyle.Render("[LLM ("+currentModel+")]:")+"\n"+msg.text)
		}

		wrappedText := lipgloss.NewStyle().Width(m.viewport.Width).Render(strings.Join(m.messages, "\n"))
		m.viewport.SetContent(wrappedText)
		m.viewport.GotoBottom()
		return m, nil
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

func (m model) View() string {
	currentModel := m.models[m.activeModel]
	title := titleStyle.Render(fmt.Sprintf(" Reflex Chaos Dojo [%s] ", currentModel))
	
	vpView := borderStyle.Render(m.viewport.View())
	taView := borderStyle.Render(m.textarea.View())

	return lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		vpView,
		taView,
	)
}

// Start launches the Bubbletea TUI
func Start() error {
	fmt.Println("Initializing Reflex TUI...")
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
