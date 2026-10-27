// Package mcp 实现 MCP（Model Context Protocol）2026-07-28 规范的无状态 HTTP 入口。
//
// 无状态意味着：不握手、不发 Mcp-Session-Id、不要求 initialize；每个请求自带全部上下文，
// 工具调用直接作用于 registry 中的书源实例（实例 id 由 source_create 返回）。
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"legado-go/internal/booksource"
	"legado-go/internal/model"
	"legado-go/internal/registry"
)

// ProtocolVersion 是实现的 MCP 协议版本
const ProtocolVersion = "2026-07-28"

// Server 是 MCP 端点的处理器
type Server struct {
	reg     *registry.Registry
	name    string
	version string
	log     func(format string, args ...any)
}

// Options 是 MCP 服务配置
type Options struct {
	Name    string
	Version string
	Log     func(format string, args ...any)
}

// New 创建 MCP 服务
func New(reg *registry.Registry, opts Options) *Server {
	if opts.Name == "" {
		opts.Name = "legado-booksource"
	}
	if opts.Version == "" {
		opts.Version = "1.0.0"
	}
	return &Server{reg: reg, name: opts.Name, version: opts.Version, log: opts.Log}
}

// rpcRequest 是 JSON-RPC 2.0 请求
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse 是 JSON-RPC 2.0 响应
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ServeHTTP 处理 MCP 的 POST 请求（无状态，无 GET/SSE 通道）
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed: MCP endpoint is stateless, use POST", http.StatusMethodNotAllowed)
		return
	}
	if v := strings.TrimSpace(r.Header.Get("Mcp-Protocol-Version")); v != "" && v != ProtocolVersion {
		http.Error(w, fmt.Sprintf("unsupported MCP-Protocol-Version %q (server supports %s)", v, ProtocolVersion), http.StatusBadRequest)
		return
	}
	accept := r.Header.Get("Accept")
	if accept != "" && !strings.Contains(accept, "application/json") && !strings.Contains(accept, "*/*") {
		http.Error(w, "Accept must include application/json", http.StatusNotAcceptable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Protocol-Version", ProtocolVersion)

	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var batch []rpcRequest
		if err := json.Unmarshal(body, &batch); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
			return
		}
		out := make([]rpcResponse, 0, len(batch))
		for _, req := range batch {
			if resp, hasResp := s.handle(req); hasResp {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
		return
	}
	resp, hasResp := s.handle(req)
	if !hasResp {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) handle(req rpcRequest) (rpcResponse, bool) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "invalid jsonrpc version"}}, true
	}
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo":   map[string]any{"name": s.name, "version": s.version},
			"instructions": instructions,
		}}, true
	case "notifications/initialized", "notifications/cancelled":
		return rpcResponse{}, false
	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}, true
	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": toolDefinitions()}}, true
	case "tools/call":
		return s.callTool(req), true
	case "resources/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resources": []any{}}}, true
	case "prompts/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"prompts": []any{}}}, true
	default:
		if isNotification {
			return rpcResponse{}, false
		}
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}, true
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) callTool(req rpcRequest) rpcResponse {
	var params callParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}}
	}
	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	result, err := s.dispatch(params.Name, args)
	if err != nil {
		kind := model.KindOf(err)
		text := err.Error()
		var me *model.Error
		if errors.As(err, &me) {
			text = me.Msg
		}
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: toolErrorResult(kind, text)}
	}
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: toolResult(result)}
}

func toolResult(payload any) map[string]any {
	text, _ := json.Marshal(payload)
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(text)}},
		"structuredContent": payload,
		"isError":           false,
	}
}

func toolErrorResult(kind, message string) map[string]any {
	payload := map[string]any{"error": map[string]any{"kind": kind, "message": message}}
	text, _ := json.Marshal(payload)
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(text)}},
		"structuredContent": payload,
		"isError":           true,
	}
}

func (s *Server) dispatch(name string, raw json.RawMessage) (any, error) {
	raw = normalizeArgs(raw)
	switch name {
	case "source_create":
		var args struct {
			ID         string          `json:"id"`
			Config     json.RawMessage `json:"config"`
			TTL        json.RawMessage `json:"ttl"`
			TTLSeconds int             `json:"ttlSeconds"`
			Name       string          `json:"name"`
			Owner      string          `json:"owner"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		cfg, err := model.ParseConfig(args.Config)
		if err != nil {
			return nil, err
		}
		if args.Name != "" {
			cfg.BookSourceName = args.Name
		}
		ttl, err := parseTTL(args.TTL, args.TTLSeconds)
		if err != nil {
			return nil, err
		}
		entry, err := s.reg.CreateWithID(strings.TrimSpace(args.ID), cfg, ttl, args.Owner)
		if err != nil {
			return nil, err
		}
		return map[string]any{"instance": entry.Info()}, nil

	case "source_validate":
		var args struct {
			Config json.RawMessage `json:"config"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		cfg, err := model.ParseConfig(args.Config)
		if err != nil {
			return nil, err
		}
		normalized, err := model.Normalize(cfg)
		if err != nil {
			return nil, err
		}
		var probe *booksource.Source
		probe, err = booksource.New(cfg, booksource.Options{})
		if err != nil {
			return nil, err
		}
		defer probe.Close()
		return map[string]any{
			"valid":          true,
			"name":           normalized.Name,
			"url":            normalized.URL,
			"group":          normalized.Group,
			"type":           normalized.Type,
			"hasSearch":      normalized.SearchURL != "",
			"exploreEntries": len(normalized.ExploreEntries),
			"variables":      len(normalized.Variable),
		}, nil

	case "source_list":
		return map[string]any{"instances": s.reg.List(), "stats": s.reg.Stats()}, nil

	case "source_get":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		entry, err := s.reg.Lookup(args.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"instance":       entry.Info(),
			"variables":      entry.Source.Variables(),
			"exploreEntries": entry.Source.ExploreEntries(),
		}, nil

	case "source_delete":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		if err := s.reg.Delete(args.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": args.ID}, nil

	case "source_touch":
		var args struct {
			ID         string          `json:"id"`
			InstanceID string          `json:"instanceId"`
			TTL        json.RawMessage `json:"ttl"`
			TTLSeconds int             `json:"ttlSeconds"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		ttl, err := parseTTL(args.TTL, args.TTLSeconds)
		if err != nil {
			return nil, err
		}
		entry, err := s.reg.Touch(firstNonEmpty(args.InstanceID, args.ID), ttl)
		if err != nil {
			return nil, err
		}
		return map[string]any{"instance": entry.Info()}, nil

	case "source_search":
		var args struct {
			ID   string `json:"id"`
			Key  string `json:"key"`
			Page int    `json:"page"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		src, err := s.source(args.ID)
		if err != nil {
			return nil, err
		}
		start := time.Now()
		books, err := src.Search(args.Key, args.Page)
		if err != nil {
			return nil, err
		}
		return map[string]any{"books": books, "count": len(books), "elapsedMs": time.Since(start).Milliseconds()}, nil

	case "source_explore":
		var args struct {
			ID   string `json:"id"`
			URL  string `json:"url"`
			Page int    `json:"page"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		src, err := s.source(args.ID)
		if err != nil {
			return nil, err
		}
		start := time.Now()
		books, err := src.Explore(args.URL, args.Page)
		if err != nil {
			return nil, err
		}
		return map[string]any{"books": books, "count": len(books), "elapsedMs": time.Since(start).Milliseconds()}, nil

	case "source_book_info":
		var args bookArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		src, err := s.source(args.instance())
		if err != nil {
			return nil, err
		}
		book, err := src.GetBookInfo(args.toBook())
		if err != nil {
			return nil, err
		}
		return map[string]any{"book": book}, nil

	case "source_chapters":
		var args bookArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		src, err := s.source(args.instance())
		if err != nil {
			return nil, err
		}
		chapters, err := src.GetChapterList(args.toBook())
		if err != nil {
			return nil, err
		}
		return map[string]any{"chapters": chapters, "count": len(chapters)}, nil

	case "source_content":
		var args chapterArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		src, err := s.source(args.instance())
		if err != nil {
			return nil, err
		}
		chapter := args.toChapter()
		book := args.toBook()
		if book.BookURL == "" {
			book.BookURL = chapter.BookURL
		}
		result, err := src.GetContent(chapter, book)
		if err != nil {
			return nil, err
		}
		return map[string]any{"content": result}, nil

	case "source_eval":
		var args struct {
			ID      string `json:"id"`
			Code    string `json:"code"`
			Script  string `json:"script"`
			Result  any    `json:"result"`
			BaseURL string `json:"baseUrl"`
			Key     string `json:"key"`
			Page    int    `json:"page"`
			Title   string `json:"title"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, model.WrapError(model.KindInvalidConfig, err, "invalid arguments")
		}
		src, err := s.source(args.ID)
		if err != nil {
			return nil, err
		}
		code := args.Script
		if code == "" {
			code = args.Code
		}
		out, err := src.EvalJS(code, booksource.Bindings{
			BaseURL: args.BaseURL,
			Key:     args.Key,
			Page:    args.Page,
			Title:   args.Title,
			Result:  args.Result,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": out}, nil

	default:
		return nil, model.NewError(model.KindInvalidConfig, "unknown tool %q", name)
	}
}

func (s *Server) source(id string) (*booksource.Source, error) {
	if strings.TrimSpace(id) == "" {
		return nil, model.NewError(model.KindInvalidConfig, "id is required")
	}
	entry, err := s.reg.Get(id)
	if err != nil {
		return nil, err
	}
	return entry.Source, nil
}

// instructions 是 initialize 返回给客户端的用法说明（MCP 2026-07-28 可选字段）。
const instructions = `legado 书源调用服务。先 tools/call source_create 传入 {config: 书源JSON, ttlSeconds?: 存活秒} 拿到 instance.id，
之后用同一 instance.id 调 source_search / source_explore / source_book_info / source_chapters / source_content / source_eval。
实例按 TTL 空闲过期：每次成功调用都会续期；过期后再调用返回 kind=EXPIRED，需要重新 source_create。
book/chapter 参数可直接回传上一步的响应对象。`

// normalizeArgs 把 instanceId 归一化成 id，兼容两种命名。
func normalizeArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	if _, ok := m["id"]; ok {
		return raw
	}
	v, ok := m["instanceId"]
	if !ok {
		return raw
	}
	m["id"] = v
	if b, err := json.Marshal(m); err == nil {
		return b
	}
	return raw
}
