# una
una not asciinema, a terminal streaming tool, recorder and player.

# Usage
```sh
una -live # Live streaming your terminal session
una -live -port 8081 # Live streaming on port 8081(default is 8080)
una -record demo.json # Record your terminal session to demo.json
una -live -record demo.json # Live streaming and recording simultaneously
una -play demo.json # Play back the recorded terminal session from demo.json
```

# Dependencies
[x/term](https://pkg.go.dev/golang.org/x/term)
[creack/pty](https://pkg.go.dev/github.com/creack/pty)
[gorilla/websocket](https://pkg.go.dev/github.com/gorilla/websocket)

# Building
```
$ go build
$ go install .
```

## Todo Features
- User system for streaming and chatting
- Allow posting of recorded sessions to a server
- Replay sessions in website
- Convert recording sessions into GIFs or videos

# Contributions
Contributions are welcomed, feel free to open a pull request.

# License
This project is licensed under the GNU Public License v3.0. See [LICENSE](https://github.com/night0721/una/blob/master/LICENSE) for more information.
