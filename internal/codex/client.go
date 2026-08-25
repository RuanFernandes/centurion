package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	ErrNotConnected = errors.New("codex app-server is not connected")
	ErrClosed       = errors.New("codex app-server connection closed")
)

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == 0 {
		return e.Message
	}
	return fmt.Sprintf("codex rpc error %d: %s", e.Code, e.Message)
}

type Notification struct {
	Method string
	Params json.RawMessage
}

type ServerRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

type rpcResponse struct {
	Result json.RawMessage
	Error  *RPCError
	Err    error
}

type wireMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type Client struct {
	command string
	args    []string

	mu            sync.Mutex
	writeMu       sync.Mutex
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	done          chan struct{}
	doneOnce      sync.Once
	processStop   context.CancelFunc
	pending       map[string]chan rpcResponse
	subscribers   map[uint64]chan Notification
	nextSubID     uint64
	nextReqID     atomic.Uint64
	serverHandler func(ServerRequest)

	diagnosticsMu  sync.Mutex
	diagnostics    []string
	diagnosticsCap int
}

func NewClient(command string, args ...string) *Client {
	return newClient(command, append([]string{"app-server"}, args...))
}

func newRawClient(command string, args ...string) *Client {
	return newClient(command, args)
}

func newClient(command string, args []string) *Client {
	if strings.TrimSpace(command) == "" {
		command = "codex"
	}
	return &Client{
		command:        command,
		args:           append([]string(nil), args...),
		pending:        make(map[string]chan rpcResponse),
		subscribers:    make(map[uint64]chan Notification),
		diagnosticsCap: 200,
	}
}

func (c *Client) Start(parent context.Context) error {
	c.mu.Lock()
	if c.cmd != nil {
		c.mu.Unlock()
		return nil
	}
	command, err := exec.LookPath(c.command)
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("find codex executable: %w", err)
	}
	processCtx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(processCtx, command, c.args...)
	configureHiddenProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		c.mu.Unlock()
		return fmt.Errorf("open codex stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		c.mu.Unlock()
		return fmt.Errorf("open codex stderr: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		c.mu.Unlock()
		return fmt.Errorf("open codex stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		c.mu.Unlock()
		return fmt.Errorf("start codex app-server: %w", err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.done = make(chan struct{})
	c.doneOnce = sync.Once{}
	c.processStop = cancel
	c.mu.Unlock()

	go c.readStdout(stdout)
	go c.readStderr(stderr)
	go func() {
		err := cmd.Wait()
		c.finish(err)
	}()
	return nil
}

func (c *Client) Stop() error {
	c.mu.Lock()
	cmd := c.cmd
	stdin := c.stdin
	stop := c.processStop
	c.mu.Unlock()
	if cmd == nil {
		return nil
	}
	if stop != nil {
		stop()
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil && c.done != nil
}

func (c *Client) Command() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.command
}

func (c *Client) PID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

func (c *Client) Diagnostics() []string {
	c.diagnosticsMu.Lock()
	defer c.diagnosticsMu.Unlock()
	return append([]string(nil), c.diagnostics...)
}

func (c *Client) SetServerRequestHandler(handler func(ServerRequest)) {
	c.mu.Lock()
	c.serverHandler = handler
	c.mu.Unlock()
}

func (c *Client) Subscribe() (<-chan Notification, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	channel := make(chan Notification, 256)
	id := c.nextSubID
	c.nextSubID++
	c.subscribers[id] = channel
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			c.mu.Lock()
			if current, ok := c.subscribers[id]; ok {
				delete(c.subscribers, id)
				close(current)
			}
			c.mu.Unlock()
		})
	}
	return channel, cancel
}

func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if strings.TrimSpace(method) == "" {
		return errors.New("rpc method is required")
	}
	c.mu.Lock()
	if c.cmd == nil || c.done == nil {
		c.mu.Unlock()
		return ErrNotConnected
	}
	id := strconv.FormatUint(c.nextReqID.Add(1), 10)
	responseChannel := make(chan rpcResponse, 1)
	c.pending[id] = responseChannel
	done := c.done
	c.mu.Unlock()

	message := wireMessage{ID: json.RawMessage(id), Method: method}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			c.removePending(id)
			return fmt.Errorf("encode params for %s: %w", method, err)
		}
		message.Params = encoded
	}
	if err := c.write(message); err != nil {
		c.removePending(id)
		return err
	}

	select {
	case response := <-responseChannel:
		if response.Err != nil {
			return response.Err
		}
		if response.Error != nil {
			return response.Error
		}
		if result == nil || len(response.Result) == 0 || string(response.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode result for %s: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-done:
		c.removePending(id)
		return ErrClosed
	}
}

func (c *Client) Notify(ctx context.Context, method string, params any) error {
	if strings.TrimSpace(method) == "" {
		return errors.New("rpc method is required")
	}
	message := wireMessage{Method: method}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encode notification %s: %w", method, err)
		}
		message.Params = encoded
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return c.write(message)
}

func (c *Client) Respond(ctx context.Context, id json.RawMessage, result any, rpcErr *RPCError) error {
	if len(id) == 0 || string(id) == "null" {
		return errors.New("server request id is required")
	}
	message := wireMessage{ID: append(json.RawMessage(nil), id...), Error: rpcErr}
	if rpcErr == nil && result != nil {
		encoded, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode server response: %w", err)
		}
		message.Result = encoded
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return c.write(message)
}

func (c *Client) write(message wireMessage) error {
	c.mu.Lock()
	stdin := c.stdin
	done := c.done
	c.mu.Unlock()
	if stdin == nil || done == nil {
		return ErrNotConnected
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode rpc message: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-done:
		return ErrClosed
	default:
	}
	if _, err := io.WriteString(stdin, string(encoded)+"\n"); err != nil {
		return fmt.Errorf("write app-server message: %w", err)
	}
	return nil
}

func (c *Client) readStdout(reader io.Reader) {
	lineReader := bufio.NewReaderSize(reader, 64*1024)
	for {
		line, err := lineReader.ReadBytes('\n')
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) > 0 {
			c.handleLine(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.addDiagnostic(fmt.Sprintf("app-server stdout: %v", err))
			}
			return
		}
	}
}

func (c *Client) readStderr(reader io.Reader) {
	lineReader := bufio.NewReaderSize(reader, 16*1024)
	for {
		line, err := lineReader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			c.addDiagnostic(line)
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) handleLine(line []byte) {
	var message wireMessage
	if err := json.Unmarshal(line, &message); err != nil {
		c.addDiagnostic(fmt.Sprintf("invalid app-server JSONL: %v", err))
		return
	}
	if message.Method != "" {
		if hasID(message.ID) {
			c.mu.Lock()
			handler := c.serverHandler
			c.mu.Unlock()
			request := ServerRequest{ID: append(json.RawMessage(nil), message.ID...), Method: message.Method, Params: append(json.RawMessage(nil), message.Params...)}
			if handler != nil {
				go handler(request)
				return
			}
			go func() {
				_ = c.Respond(context.Background(), message.ID, nil, &RPCError{Code: -32000, Message: "client cannot handle server request"})
			}()
			return
		}
		c.publish(Notification{Method: message.Method, Params: append(json.RawMessage(nil), message.Params...)})
		return
	}
	if !hasID(message.ID) {
		return
	}
	key := idKey(message.ID)
	c.mu.Lock()
	responseChannel, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	if ok {
		responseChannel <- rpcResponse{Result: append(json.RawMessage(nil), message.Result...), Error: message.Error}
	}
}

func (c *Client) publish(notification Notification) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, channel := range c.subscribers {
		select {
		case channel <- notification:
		default:
			c.addDiagnostic("notification subscriber buffer full; event dropped")
		}
	}
}

func (c *Client) removePending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) finish(processErr error) {
	c.doneOnce.Do(func() {
		c.mu.Lock()
		if processErr != nil && !errors.Is(processErr, context.Canceled) {
			c.addDiagnostic(fmt.Sprintf("app-server exited: %v", processErr))
		}
		close(c.done)
		for id, responseChannel := range c.pending {
			delete(c.pending, id)
			responseChannel <- rpcResponse{Err: ErrClosed}
		}
		for id, channel := range c.subscribers {
			delete(c.subscribers, id)
			close(channel)
		}
		c.cmd = nil
		c.stdin = nil
		c.processStop = nil
		c.mu.Unlock()
	})
}

func (c *Client) addDiagnostic(message string) {
	message = redactDiagnostic(strings.TrimSpace(message))
	if message == "" {
		return
	}
	c.diagnosticsMu.Lock()
	defer c.diagnosticsMu.Unlock()
	c.diagnostics = append(c.diagnostics, message)
	if len(c.diagnostics) > c.diagnosticsCap {
		c.diagnostics = c.diagnostics[len(c.diagnostics)-c.diagnosticsCap:]
	}
}

func hasID(id json.RawMessage) bool {
	return len(id) > 0 && string(id) != "null"
}

func idKey(id json.RawMessage) string {
	return string(id)
}

var (
	secretPattern = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{8,}|bearer\s+[A-Za-z0-9._~+/=-]+|access_token[=:][^\s&]+)`)
)

func redactDiagnostic(message string) string {
	return secretPattern.ReplaceAllString(message, "[REDACTED]")
}
