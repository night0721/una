package main

import (
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

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

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

func broadcast(ev Event) {
	msg, _ := json.Marshal(ev)

	if record_file != nil {
		record_file.Write(msg)
		record_file.Write([]byte("\n"))
	}

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

func ws_handler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	clients_mu.Lock()
	clients[conn] = true
	clients_mu.Unlock()
}

func run_replay(path string) {
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

func run_live(port string) {
	go func() {
		http.HandleFunc("/ws", ws_handler)
		http.ListenAndServe(":"+port, nil)
	}()

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
			broadcast(Event{Type: "output", Data: data, Time: time.Now().UnixNano()})
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

func main() {
	live_mode := flag.Bool("live", false, "Stream live")
	replay_path := flag.String("replay", "", "Replay file path")
	rec_path := flag.String("record", "", "Record session to file")
	port := flag.String("port", "8080", "WebSocket port")
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
		run_replay(*replay_path)
	} else if *live_mode {
		run_live(*port)
	} else {
		flag.Usage()
	}
}
