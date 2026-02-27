package main

import (
	"fmt"
	"io"
	"path/filepath"
	"bytes"
	"bufio"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"mime/multipart"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

const UPLOAD_DIR = "./recordings"
const AUTH_TOKEN = "una_2026"

type Event struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Time int64  `json:"time"`
}

var (
	clients     = make(map[*websocket.Conn]bool)
	clients_mu  sync.Mutex
	record_file *os.File
	upgrader    = websocket.Upgrader{
		CheckOrigin:       func(r *http.Request) bool { return true },
		EnableCompression: true,
	}
)

func broadcast(ev Event, live bool) {
	msg, _ := json.Marshal(ev)

	if record_file != nil {
		record_file.Write(msg)
		record_file.Write([]byte("\n"))
	}
	
	if live {
		clients_mu.Lock()
		defer clients_mu.Unlock()

		for conn := range clients {
			err := conn.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				conn.Close()
				delete(clients, conn)
			}
		}
	}
}

func ws_handler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	clients_mu.Lock()
	clients[conn] = true
	clients_mu.Unlock()
}

func replay(path string) {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// Enter alternate screen and put cursor to top left
	os.Stdout.Write([]byte("\x1b[?1049h\x1b[H"))
	// Exit alternate screen on return
	defer os.Stdout.Write([]byte("\x1b[?1049l"))

	scanner := bufio.NewScanner(f)
	var last_time int64

	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type == "output" {
			if last_time != 0 {
				time.Sleep(time.Duration(ev.Time - last_time))
			}
			last_time = ev.Time
			os.Stdout.WriteString(ev.Data)
		}
	}
}

func record(port string, live bool) {
	if live {
		go func() {
			http.HandleFunc("/ws", ws_handler)
			http.ListenAndServe(":"+port, nil)
		}()
	}

	// Setup alternate screen and put cursor to topleft
	os.Stdout.Write([]byte("\x1b[?1049h\x1b[H"))
	defer os.Stdout.Write([]byte("\x1b[?1049l")) // Restores screen and make cursor visible
	defer os.Stdout.Write([]byte("\x1b[?25h"))

	// Setup pty
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.Command(shell)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Fatal(err)
	}
	defer ptmx.Close()

	// Set terminal to raw mode then restore it later
	old_state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		log.Fatal(err)
	}
	defer term.Restore(int(os.Stdin.Fd()), old_state)

	// Resize handler
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	ch <- syscall.SIGWINCH

	// Done channel to coordinate exit
	done := make(chan struct{})

	// PTY Output -> Stdout & WebSocket
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				// EOF detected (shell exited)
				close(done) // Signal main thread to finish
				return
			}
			data := string(buf[:n])
			os.Stdout.Write(buf[:n])
			broadcast(Event{Type: "output", Data: data, Time: time.Now().UnixNano()}, live)
		}
	}()

	// Stdin -> PTY
	go func() {
		buf_in := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf_in)
			if err != nil {
				return
			}
			ptmx.Write(buf_in[:n])
		}
	}()

	// Block here until PTY closes
	<-done
}

func server(port string) {
	mux := http.NewServeMux()

    fileServer := http.FileServer(http.Dir("./static"))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, "./static/index.html")
			return
		}
		if r.URL.Path == "/replay" {
			http.ServeFile(w, r, "./static/replay.html")
			return
		}
		if r.URL.Path == "/style.css" {
			http.ServeFile(w, r, "./static/style.css")
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	mux.Handle("/recordings/", http.StripPrefix("/recordings/", http.FileServer(http.Dir(UPLOAD_DIR))))
	mux.HandleFunc("/upload", HandleUpload)

	log.Printf("Server starting on: 0.0.0.0:" + port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+ port, mux))
}

func send_to_server(filePath string, serverURL string, apiKey string) {
	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Failed to open recording: %v", err)
		return
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create form file field
	part, err := writer.CreateFormFile("recording", filepath.Base(filePath))
	if err != nil {
		log.Printf("Failed to create form: %v", err)
		return
	}
	io.Copy(part, file)
	writer.Close()

	req, _ := http.NewRequest("POST", serverURL + "/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-API-KEY", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Upload failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		fmt.Printf("\nSession uploaded! View at: %s/replay?id=%s\n",
			serverURL, filepath.Base(filePath))
	} else {
		fmt.Printf("\nUpload failed with status: %d\n", resp.StatusCode)
	}
}

func get_auth_token() string {
	token := os.Getenv("UNA_AUTH_TOKEN")
	if token == "" {
		return "una"
	}
	return token
}

func HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}

	if r.Header.Get("X-API-KEY") != get_auth_token() {
		http.Error(w, "Unauthorized", 401)
		return
	}

	file, header, err := r.FormFile("recording")
	if err != nil {
		http.Error(w, "No file provided", 400)
		return
	}
	defer file.Close()

	// Ensure upload dir exists
	os.MkdirAll(UPLOAD_DIR, 0755)

	// Use a clean filename to prevent directory traversal attacks
	safeName := filepath.Base(header.Filename)
	dst, err := os.Create(filepath.Join(UPLOAD_DIR, safeName))
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	defer dst.Close()

	io.Copy(dst, file)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func main() {
	upload_path := flag.String("upload", "", "Upload recording")
	server_url := flag.String("url", "", "Server URL")
	server_mode := flag.Bool("server", false, "Start server")
	live_mode := flag.Bool("live", false, "Stream live")
	replay_path := flag.String("replay", "", "Replay file path")
	rec_path := flag.String("record", "", "Record session to file")
	wsport := flag.String("wsport", "8080", "WebSocket port")
	serverport := flag.String("serverport", "8081", "Server port")
	flag.Parse()

	if *rec_path != "" {
		f, err := os.Create(*rec_path)
		if err != nil {
			log.Fatal(err)
		}
		record_file = f
		defer record_file.Close()
	}

	if *replay_path != "" {
		replay(*replay_path)
	} else if *live_mode {
		record(*wsport, true)	
	} else if *server_mode {
		server(*serverport)	
	} else if (*upload_path != ""  && *server_url != "" && *serverport != "") {
		send_to_server(*upload_path, *server_url + ":" + *serverport, get_auth_token())
	} else {
		if *rec_path != "" {
			record(*wsport, false)
		} else {
			flag.Usage()
		}
	}
}
