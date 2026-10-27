package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	openRouterURL = "https://openrouter.ai/api/v1/chat/completions"
	googleAIURL   = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"

	openRouterKeyFile = "/home/portassh/openrouter.key"
	googleAIKeyFile   = "/home/portassh/google_ai.key"
)

// Модели по умолчанию
var defaultFreeModelsOpenRouter = []string{
	"openrouter/auto",
	"openrouter/free",
	"nvidia/nemotron-3.5-lightning:free",
	"recursive/flint-v1.0:free",
	"cohere/north-mini-code:free",
	"google/gemma-4-31b-it:free",
	"google/gemma-4-26b-a4b-it:free",
	"nvidia/nemotron-3-super-120b-a12b:free",
	"nvidia/nemotron-3-ultra-550b-a55b:free",
	"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free",
	"nvidia/nemotron-3.5-super-vl:free",
	"liquid/lfm-2.5-2.6b:free",
	"thinkingmachines/inkling:free",
	"thinkingmachines/inkling-small:free",
	"xingchen-agi/xing4.0-29b-a4b:free",
	"dots-studio/dots-3-note-preview:free",
	"poolside/laguna-s-2.1:free",
	"poolside/laguna-xs-2.1:free",
}

var defaultGoogleAIModels = []string{
	"gemini-2.5-flash",
	"gemini-2.5-pro",
	"gemini-flash-latest",
	"gemini-flash-lite-latest",
}

// aiErr: ошибки AI-эндпоинтов в JSON — дашборд парсит ответ как json.
func aiErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// fallbackModel: надёжная модель провайдера на случай смерти выбранной
// (openrouter/auto сам выбирает рабочий маршрут; для Google — актуальный flash).
func fallbackModel(provider string) string {
	if provider == "google" {
		return "gemini-flash-latest"
	}
	return "openrouter/auto"
}

var (
	cachedFreeModelsOpenRouter []string
	cachedFreeModelsMu         sync.RWMutex
	cachedFreeModelsTime       time.Time
)

func getLiveOpenRouterFreeModels() []string {
	cachedFreeModelsMu.RLock()
	if len(cachedFreeModelsOpenRouter) > 0 && time.Since(cachedFreeModelsTime) < 30*time.Minute {
		res := make([]string, len(cachedFreeModelsOpenRouter))
		copy(res, cachedFreeModelsOpenRouter)
		cachedFreeModelsMu.RUnlock()
		return res
	}
	cachedFreeModelsMu.RUnlock()

	// Запрашиваем каталог моделей OpenRouter
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", "https://openrouter.ai/api/v1/models", nil)
	if err == nil {
		ork := readAPIKey("", openRouterKeyFile, "OPENROUTER_API_KEY")
		if ork != "" {
			req.Header.Set("Authorization", "Bearer "+ork)
		}
		if resp, err := client.Do(req); err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			var mResp struct {
				Data []struct {
					ID      string `json:"id"`
					Pricing struct {
						Prompt     any `json:"prompt"`
						Completion any `json:"completion"`
					} `json:"pricing"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&mResp); err == nil {
				var list []string
				list = append(list, "openrouter/auto")
				for _, m := range mResp.Data {
					if m.ID == "openrouter/auto" {
						continue
					}
					isFree := strings.HasSuffix(m.ID, ":free") || strings.Contains(m.ID, ":free")
					if !isFree {
						pStr := fmt.Sprintf("%v", m.Pricing.Prompt)
						cStr := fmt.Sprintf("%v", m.Pricing.Completion)
						if (pStr == "0" || pStr == "0.0") && (cStr == "0" || cStr == "0.0") && !strings.Contains(m.ID, "lyria") {
							isFree = true
						}
					}
					if isFree {
						list = append(list, m.ID)
					}
				}
				if len(list) > 1 {
					cachedFreeModelsMu.Lock()
					cachedFreeModelsOpenRouter = list
					cachedFreeModelsTime = time.Now()
					cachedFreeModelsMu.Unlock()
					return list
				}
			}
		}
	}

	return defaultFreeModelsOpenRouter
}

type aiConfigResponse struct {
	OpenRouterKeySet bool     `json:"openrouter_key_set"`
	GoogleAIKeySet   bool     `json:"google_ai_key_set"`
	OpenRouterModels []string `json:"openrouter_models"`
	GoogleAIModels   []string `json:"google_ai_models"`
}

func readAPIKey(customKey, filePath, envVar string) string {
	if customKey = strings.TrimSpace(customKey); customKey != "" {
		return customKey
	}
	if b, err := os.ReadFile(filePath); err == nil {
		if k := strings.TrimSpace(string(b)); k != "" {
			return k
		}
	}
	return strings.TrimSpace(os.Getenv(envVar))
}

// aiConfigHandler: список моделей и статус ключей
func aiConfigHandler(w http.ResponseWriter, r *http.Request) {
	ork := readAPIKey("", openRouterKeyFile, "OPENROUTER_API_KEY")
	gak := readAPIKey("", googleAIKeyFile, "GEMINI_API_KEY")
	resp := aiConfigResponse{
		OpenRouterKeySet: ork != "",
		GoogleAIKeySet:   gak != "",
		OpenRouterModels: getLiveOpenRouterFreeModels(),
		GoogleAIModels:   defaultGoogleAIModels,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// eventsHandler: сбор ошибок журналов Windows с целевой машины
func eventsHandler(w http.ResponseWriter, r *http.Request) {
	portStr := r.URL.Query().Get("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1024 || port > 65535 {
		aiErr(w, "bad port", 400)
		return
	}
	ch := r.URL.Query().Get("channel")
	if ch == "" {
		ch = "System"
	}
	cmd := "sys:events " + ch
	code, out := execOnTarget(port, cmd)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"port":    port,
		"channel": ch,
		"code":    code,
		"raw":     out,
	})
}

// explainEventsRequest: запрос на анализ журнала
type explainEventsRequest struct {
	Provider string `json:"provider"` // "openrouter" | "google"
	Model    string `json:"model"`
	ApiKey   string `json:"api_key,omitempty"`
	Port     int    `json:"port"`
	Channel  string `json:"channel"`
	Events   string `json:"events,omitempty"`
}

func explainEventsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		aiErr(w, "method not allowed", 405)
		return
	}
	var req explainEventsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		aiErr(w, "bad request: "+err.Error(), 400)
		return
	}
	if req.Channel == "" {
		req.Channel = "System"
	}
	eventsText := strings.TrimSpace(req.Events)
	if eventsText == "" && req.Port >= 1024 {
		_, out := execOnTarget(req.Port, "sys:events "+req.Channel)
		eventsText = out
	}
	if eventsText == "" {
		aiErr(w, "no events to analyze", 400)
		return
	}

	apiKey, apiURL, model := resolveProvider(req.Provider, req.Model, req.ApiKey)
	if apiKey == "" {
		aiErr(w, "API ключ для провайдера "+req.Provider+" не настроен", 401)
		return
	}

	sysPrompt := "Ты — Senior Windows Systems Engineer и специалист по расследованию инцидентов. " +
		"Твоя задача — проанализировать реальный фрагмент журнала событий Windows (" + req.Channel + ").\n" +
		"Правила анализа:\n" +
		"1. Отсей системный фоновый шум и безобидные предупреждения (DCOM 10016, сетевые таймауты браузеров).\n" +
		"2. Выдели РЕАЛЬНЫЕ критические проблемы (сбои диска, драйверов, памяти, служб, криптопровайдеров, баз данных).\n" +
		"3. Для каждой реальной проблемы укажи:\n" +
		"   - Event ID и Источник\n" +
		"   - Понятную суть сбоя человеческим языком\n" +
		"   - Точную команду для исправления или диагностический шаг (PowerShell / cmd).\n" +
		"Отвечай структурированно, строго на русском языке в формате Markdown."

	userMsg := fmt.Sprintf("Вот последние события из журнала %s машины (порт %d):\n\n```\n%s\n```\n\nПроанализируй эти сбои и дай чёткие рекомендации.",
		req.Channel, req.Port, eventsText)

	messages := []chatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userMsg},
	}

	payload := openAIReq{
		Model:       model,
		Messages:    messages,
		Temperature: 0.2,
	}

	reply, err := callAICompletions(r.Context(), apiURL, apiKey, payload, req.Provider)
	if err != nil {
		// free-модели провайдеров отваливаются на стороне OpenRouter (404 Not Found) —
		// повторяем с дефолтной моделью, которая выбирает рабочий маршрут сама
		if fb := fallbackModel(req.Provider); fb != "" && fb != model {
			log.Printf("ai: модель %s недоступна (%v) — фолбэк на %s", model, err, fb)
			payload.Model = fb
			reply, err = callAICompletions(r.Context(), apiURL, apiKey, payload, req.Provider)
		}
	}
	if err != nil {
		aiErr(w, "AI request failed: "+err.Error(), 502)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"provider": req.Provider,
		"model":    model,
		"analysis": reply.Choices[0].Message.Content,
	})
}

// --- Tool Calling Copilot ---

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolCallFunc `json:"function"`
}

type toolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolDef struct {
	Type     string      `json:"type"`
	Function toolDefFunc `json:"function"`
}

type toolDefFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type openAIReq struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Tools       []toolDef     `json:"tools,omitempty"`
	Temperature float64       `json:"temperature"`
}

type openAIResp struct {
	Choices []struct {
		Message struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

func getSysAdminTools() []toolDef {
	return []toolDef{
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "list_machines",
				Description: "Получить список активных машин флота portassh: порты, роли, имена, статус дисков, памяти и аптайма",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "get_machine_info",
				Description: "Получить подробный инвентарь машины: модель ПК, процессор, RAM, диски, сетевые адаптеры, установленный софт и антивирус",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"port": map[string]interface{}{"type": "integer", "description": "Порт машины (например 2200)"},
					},
					"required": []string{"port"},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "get_processes",
				Description: "Получить список процессов целевой машины с PID, именем и потреблением памяти (sys:procs)",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"port": map[string]interface{}{"type": "integer", "description": "Порт машины"},
					},
					"required": []string{"port"},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "kill_process",
				Description: "Завершить зависший процесс на машине по PID (sys:kill)",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"port": map[string]interface{}{"type": "integer", "description": "Порт машины"},
						"pid":  map[string]interface{}{"type": "integer", "description": "PID процесса"},
					},
					"required": []string{"port", "pid"},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "get_services",
				Description: "Получить список служб Windows и их статус (RUNNING, STOPPED)",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"port": map[string]interface{}{"type": "integer", "description": "Порт машины"},
					},
					"required": []string{"port"},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "manage_service",
				Description: "Управление службой Windows: запуск (start), остановка (stop) или перезапуск (restart)",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"port":   map[string]interface{}{"type": "integer", "description": "Порт машины"},
						"name":   map[string]interface{}{"type": "string", "description": "Имя службы (например Spooler)"},
						"action": map[string]interface{}{"type": "string", "enum": []string{"start", "stop", "restart"}},
					},
					"required": []string{"port", "name", "action"},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "get_event_logs",
				Description: "Считать последние ошибки и предупреждения из системных журналов Windows (System или Application)",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"port":    map[string]interface{}{"type": "integer", "description": "Порт машины"},
						"channel": map[string]interface{}{"type": "string", "enum": []string{"System", "Application"}, "description": "Имя журнала"},
					},
					"required": []string{"port"},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "exec_command",
				Description: "Выполнить диагностическую или системную команду на целевой машине через шелл",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"port": map[string]interface{}{"type": "integer", "description": "Порт машины"},
						"cmd":  map[string]interface{}{"type": "string", "description": "Команда (например ipconfig /all, whoami)"},
					},
					"required": []string{"port", "cmd"},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "web_search",
				Description: "Поиск в интернете (DuckDuckGo) — для кодов ошибок Windows, KB-статей, драйверов, известной документации",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{"type": "string", "description": "Поисковый запрос (лучше на английском, например Windows error 0x80070005 fix)"},
					},
					"required": []string{"query"},
				},
			},
		},
		{
			Type: "function",
			Function: toolDefFunc{
				Name:        "web_fetch",
				Description: "Прочитать веб-страницу по URL и получить её текст (без разметки) — для изучения найденной статьи/инструкции",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"url": map[string]interface{}{"type": "string", "description": "Полный URL страницы (https://...)"},
					},
					"required": []string{"url"},
				},
			},
		},
	}
}

type aiChatRequest struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	ApiKey   string        `json:"api_key,omitempty"`
	Messages []chatMessage `json:"messages"`
}

type toolExecLog struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
	Res  string `json:"res"`
}

func executeTool(name, argsJSON string) string {
	var args map[string]interface{}
	_ = json.Unmarshal([]byte(argsJSON), &args)

	port := 0
	if p, ok := args["port"].(float64); ok {
		port = int(p)
	}

	switch name {
	case "list_machines":
		data, err := os.ReadFile(file)
		if err != nil {
			return "ошибка чтения реестра: " + err.Error()
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		var validLines []string
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				validLines = append(validLines, l)
			}
		}
		if len(validLines) > 20 {
			validLines = validLines[len(validLines)-20:]
		}
		return strings.Join(validLines, "\n")

	case "get_machine_info":
		if port < 1024 {
			return "не указан или неверен порт"
		}
		code, out := execOnTarget(port, "sys:info")
		if code != 0 && out == "" {
			return fmt.Sprintf("ошибка выполнения sys:info (код %d)", code)
		}
		return out

	case "get_processes":
		if port < 1024 {
			return "не указан порт"
		}
		code, out := execOnTarget(port, "sys:procs")
		if code != 0 && out == "" {
			return fmt.Sprintf("ошибка получения процессов (код %d)", code)
		}
		return out

	case "kill_process":
		pid, _ := args["pid"].(float64)
		if port < 1024 || pid <= 0 {
			return "не указан порт или pid"
		}
		code, out := execOnTarget(port, fmt.Sprintf("sys:kill %d", int(pid)))
		return fmt.Sprintf("код: %d, ответ: %s", code, out)

	case "get_services":
		if port < 1024 {
			return "не указан порт"
		}
		code, out := execOnTarget(port, "sys:svcs")
		return fmt.Sprintf("код: %d, ответ: %s", code, out)

	case "manage_service":
		action, _ := args["action"].(string)
		sname, _ := args["name"].(string)
		if port < 1024 || action == "" || sname == "" {
			return "не указан порт, служба или действие"
		}
		code, out := execOnTarget(port, fmt.Sprintf("sys:svc-%s %s", action, sname))
		return fmt.Sprintf("код: %d, ответ: %s", code, out)

	case "get_event_logs":
		ch, _ := args["channel"].(string)
		if ch == "" {
			ch = "System"
		}
		if port < 1024 {
			return "не указан порт"
		}
		code, out := execOnTarget(port, fmt.Sprintf("sys:events %s 15", ch))
		return fmt.Sprintf("код: %d, события:\n%s", code, out)

	case "exec_command":
		cmd, _ := args["cmd"].(string)
		if port < 1024 || cmd == "" {
			return "не указан порт или команда"
		}
		code, out := execOnTarget(port, cmd)
		return fmt.Sprintf("код: %d, вывод:\n%s", code, out)

	case "web_search":
		q, _ := args["query"].(string)
		if strings.TrimSpace(q) == "" {
			return "пустой запрос"
		}
		return webSearch(q)

	case "web_fetch":
		u, _ := args["url"].(string)
		if strings.TrimSpace(u) == "" {
			return "не указан URL"
		}
		return webFetch(u)

	default:
		return "неизвестный инструмент: " + name
	}
}

var (
	reScript  = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>`)
	reStyle   = regexp.MustCompile(`(?s)<style[^>]*>.*?</style>`)
	reTag     = regexp.MustCompile(`<[^>]+>`)
	reSpaces  = regexp.MustCompile(`\s+`)
	reDDGLink = regexp.MustCompile(`(?s)class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	reDDGSnip = regexp.MustCompile(`(?s)class="result__snippet"[^>]*>(.*?)</a>`)
)

// webSearch: поиск через DuckDuckGo HTML (без API-ключей), топ-6 результатов.
func webSearch(q string) string {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", "https://html.duckduckgo.com/html/?q="+url.QueryEscape(q), nil)
	if err != nil {
		return "ошибка поиска: " + err.Error()
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := client.Do(req)
	if err != nil {
		return "ошибка поиска: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))

	titles := reDDGLink.FindAllStringSubmatch(string(body), -1)
	snips := reDDGSnip.FindAllStringSubmatch(string(body), -1)
	strip := func(s string) string {
		return strings.TrimSpace(reTag.ReplaceAllString(html.UnescapeString(s), ""))
	}
	var sb strings.Builder
	for i, m := range titles {
		if i >= 6 {
			break
		}
		href := m[1]
		if u, e := url.QueryUnescape(hrefParam(href, "uddg")); e == nil && u != "" {
			href = u
		}
		snip := ""
		if i < len(snips) {
			snip = strip(snips[i][1])
		}
		if len(snip) > 300 {
			snip = snip[:300] + "…"
		}
		sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n   %s\n", i+1, strip(m[2]), href, snip))
	}
	if sb.Len() == 0 {
		return "поиск не дал результатов"
	}
	return "Результаты поиска:\n" + sb.String()
}

// webFetch: скачать страницу по URL и вернуть текст без HTML-разметки. и вернуть текст без HTML-разметки.
func webFetch(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "неверный URL"
	}
	host := u.Hostname()
	// SSRF-щит: только публичные адреса — loopback/локальные сети запрещены
	if host == "localhost" || !strings.Contains(host, ".") ||
		strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "169.254.") ||
		strings.HasPrefix(host, "0.0.0.0") || host == "::1" ||
		(strings.HasPrefix(host, "172.") && private172(host)) {
		return "доступ к локальным адресам запрещён"
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return "ошибка: " + err.Error()
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := client.Do(req)
	if err != nil {
		return "ошибка загрузки: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	text := reScript.ReplaceAllString(string(body), " ")
	text = reStyle.ReplaceAllString(text, " ")
	text = reTag.ReplaceAllString(text, " ")
	text = reSpaces.ReplaceAllString(html.UnescapeString(text), " ")
	if len(text) > 6000 {
		text = text[:6000] + "…"
	}
	return text
}

func private172(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return false
	}
	n, err := strconv.Atoi(parts[1])
	return err == nil && n >= 16 && n <= 31
}

// hrefParam: вытащить параметр из DDG-редиректа вида ...?uddg=<enc>&rut=...
func hrefParam(href, key string) string {
	i := strings.Index(href, key+"=")
	if i < 0 {
		return ""
	}
	v := href[i+len(key)+1:]
	if j := strings.Index(v, "&"); j >= 0 {
		v = v[:j]
	}
	return v
}

func resolveProvider(provider, model, customKey string) (apiKey, apiURL, resolvedModel string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "google" || provider == "gemini" || provider == "google_ai" {
		apiKey = readAPIKey(customKey, googleAIKeyFile, "GEMINI_API_KEY")
		apiURL = googleAIURL
		if model == "" {
			model = "gemini-2.5-flash"
		}
		return apiKey, apiURL, model
	}
	// Default: OpenRouter
	apiKey = readAPIKey(customKey, openRouterKeyFile, "OPENROUTER_API_KEY")
	apiURL = openRouterURL
	if model == "" {
		model = "openrouter/auto"
	}
	return apiKey, apiURL, model
}

func callAICompletions(ctx context.Context, apiURL, apiKey string, req openAIReq, provider string) (*openAIResp, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	if strings.Contains(apiURL, "openrouter.ai") {
		httpReq.Header.Set("HTTP-Referer", "https://example.com")
		httpReq.Header.Set("X-Title", "portassh-ai")
	}

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var aiResp openAIResp
	if err := json.Unmarshal(respBytes, &aiResp); err != nil {
		return nil, fmt.Errorf("json parse error: %w (body: %s)", err, string(respBytes))
	}
	if aiResp.Error != nil && aiResp.Error.Message != "" {
		return nil, fmt.Errorf("API error: %s", aiResp.Error.Message)
	}
	if len(aiResp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices in response")
	}
	return &aiResp, nil
}

// aiChatHandler: Copilot Tool Calling Loop
func aiChatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		aiErr(w, "method not allowed", 405)
		return
	}
	var req aiChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		aiErr(w, "bad request: "+err.Error(), 400)
		return
	}
	if len(req.Messages) == 0 {
		aiErr(w, "no messages", 400)
		return
	}

	apiKey, apiURL, model := resolveProvider(req.Provider, req.Model, req.ApiKey)
	if apiKey == "" {
		aiErr(w, "API ключ не настроен для "+req.Provider, 401)
		return
	}

	tools := getSysAdminTools()
	toolLines := make([]string, 0, len(tools))
	for _, t := range tools {
		toolLines = append(toolLines, "- "+t.Function.Name+" — "+t.Function.Description)
	}
	sysPrompt := "Ты — SysAdmin Copilot: встроенный ИИ-помощник системы portassh. Твоё предназначение — помогать " +
		"оператору поддержки диагностировать и администрировать парк Windows-машин, подключённых через relay.\n\n" +
		"Твои возможности — инструменты, вызывай их для любых вопросов о машинах:\n" + strings.Join(toolLines, "\n") + "\n\n" +
		"Правила работы:\n" +
		"1. Ты работаешь в непрерывном диалоге: помни предыдущие ответы, запросы и результаты инструментов этого разговора. " +
		"Если пользователь говорит «эта машина», «она же», «а теперь у неё» — имей в виду машину из предыдущего сообщения.\n" +
		"2. Для любых фактов о машинах (состояние, диски, память, процессы, службы, журналы, команды) — ВСЕГДА вызывай инструменты, а не отвечай по памяти.\n" +
		"3. Не придумывай данные и имена машин — только то, что вернули инструменты или что явно назвал пользователь.\n" +
		"4. Отвечай кратко и чётко на русском. Давай готовые команды (PowerShell/cmd), объясняй причину проблемы простым языком.\n" +
		"5. Опасные действия (перезагрузка, удаление, завершение процессов) выполняй только по явной просьбе пользователя и предупреждай о последствиях."

	messages := make([]chatMessage, 0, len(req.Messages)+1)
	hasSystem := false
	for _, m := range req.Messages {
		if m.Role == "system" {
			hasSystem = true
		}
		messages = append(messages, m)
	}
	if !hasSystem {
		messages = append([]chatMessage{{Role: "system", Content: sysPrompt}}, messages...)
	}

	var logs []toolExecLog

	// Tool Calling Loop (до 5 итераций)
	for iter := 0; iter < 5; iter++ {
		payload := openAIReq{
			Model:       model,
			Messages:    messages,
			Tools:       tools,
			Temperature: 0.2,
		}

		aiResp, err := callAICompletions(r.Context(), apiURL, apiKey, payload, req.Provider)
		if err != nil && iter == 0 {
			// free-модели провайдеров отваливаются на стороне OpenRouter (404 Not Found) —
			// повторяем итерацию с дефолтной моделью, выбирающей рабочий маршрут сама
			if fb := fallbackModel(req.Provider); fb != "" && fb != model {
				log.Printf("ai: модель %s недоступна (%v) — фолбэк на %s", model, err, fb)
				model = fb
				payload.Model = fb
				continue
			}
		}
		if err != nil {
			log.Printf("ai: completion error iter=%d: %v", iter, err)
			aiErr(w, "AI call failed: "+err.Error(), 502)
			return
		}

		choice := aiResp.Choices[0]
		assistantMsg := chatMessage{
			Role:      choice.Message.Role,
			Content:   choice.Message.Content,
			ToolCalls: choice.Message.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		if len(choice.Message.ToolCalls) == 0 {
			// Модель сформировала финальный ответ
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"reply":       choice.Message.Content,
				"model":       model,
				"provider":    req.Provider,
				"tools_used":  logs,
				"finish_type": choice.FinishReason,
			})
			return
		}

		// Выполняем каждый tool call
		for _, tc := range choice.Message.ToolCalls {
			tRes := executeTool(tc.Function.Name, tc.Function.Arguments)
			logs = append(logs, toolExecLog{
				Tool: tc.Function.Name,
				Args: tc.Function.Arguments,
				Res:  tRes,
			})
			messages = append(messages, chatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    tRes,
			})
		}
	}

	// Если исчерпали итерации вызовов инструментов — делаем один финальный запрос на суммаризацию
	finalReq := openAIReq{
		Model:       model,
		Messages:    append(messages, chatMessage{Role: "user", Content: "Суммируй полученные факты и дай чёткий ответ на исходный запрос."}),
		Temperature: 0.2,
	}
	lastContent := ""
	if finalResp, err := callAICompletions(r.Context(), apiURL, apiKey, finalReq, req.Provider); err == nil && len(finalResp.Choices) > 0 {
		lastContent = finalResp.Choices[0].Message.Content
	} else if len(messages) > 0 {
		lastContent = messages[len(messages)-1].Content
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"reply":      lastContent,
		"model":      model,
		"provider":   req.Provider,
		"tools_used": logs,
		"iter_limit": true,
	})
}
