// Package engine is a minimal, robust UCI client for Stockfish, used both as
// the opponent and as the grader that produces the ground-truth label.
package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// MateCP is the centipawn value used to represent a forced mate.
const MateCP = 10000

// Engine is one running UCI engine process. Safe for sequential use from
// multiple goroutines (calls are serialized).
type Engine struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Scanner
	Name string
}

// Result of a search, scored from the side to move's point of view.
type Result struct {
	CP       int    // centipawns; mates are mapped to ±(MateCP - distance)
	Mate     int    // moves to mate (signed), 0 if none
	BestMove string // UCI, "" if the game is over
	Depth    int
}

// Start launches the engine binary and performs the UCI handshake.
func Start(path string, options map[string]string) (*Engine, error) {
	cmd := exec.Command(path)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w (is Stockfish installed? set --stockfish)", path, err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	e := &Engine{cmd: cmd, in: in, out: sc}

	if err := e.send("uci"); err != nil {
		return nil, err
	}
	for {
		line, err := e.readLine()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "id name ") {
			e.Name = strings.TrimPrefix(line, "id name ")
		}
		if line == "uciok" {
			break
		}
	}
	for k, v := range options {
		if err := e.send(fmt.Sprintf("setoption name %s value %s", k, v)); err != nil {
			return nil, err
		}
	}
	return e, e.sync()
}

func (e *Engine) send(s string) error {
	_, err := io.WriteString(e.in, s+"\n")
	return err
}

func (e *Engine) readLine() (string, error) {
	if !e.out.Scan() {
		if err := e.out.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return strings.TrimSpace(e.out.Text()), nil
}

func (e *Engine) sync() error {
	if err := e.send("isready"); err != nil {
		return err
	}
	for {
		line, err := e.readLine()
		if err != nil {
			return err
		}
		if line == "readyok" {
			return nil
		}
	}
}

// NewGame resets engine state between games.
func (e *Engine) NewGame() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.send("ucinewgame"); err != nil {
		return err
	}
	return e.sync()
}

// Search analyses a FEN with the given UCI "go" arguments,
// e.g. "depth 14" or "movetime 200".
func (e *Engine) Search(ctx context.Context, fen, goArgs string) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.send("position fen " + fen); err != nil {
		return Result{}, err
	}
	if err := e.send("go " + goArgs); err != nil {
		return Result{}, err
	}

	var res Result
	haveScore := false
	for {
		if ctx.Err() != nil {
			_ = e.send("stop")
		}
		line, err := e.readLine()
		if err != nil {
			return res, err
		}
		if strings.HasPrefix(line, "bestmove") {
			f := strings.Fields(line)
			if len(f) > 1 && f[1] != "(none)" {
				res.BestMove = f[1]
			}
			break
		}
		if strings.HasPrefix(line, "info ") && strings.Contains(line, " score ") {
			if r, ok := parseInfo(line); ok {
				res.CP, res.Mate, res.Depth = r.CP, r.Mate, r.Depth
				haveScore = true
			}
		}
	}
	if !haveScore && res.BestMove == "" {
		// No legal moves: the side to move is mated or stalemated.
		// The caller decides which from the position.
		return res, nil
	}
	return res, ctx.Err()
}

func parseInfo(line string) (Result, bool) {
	f := strings.Fields(line)
	var r Result
	multipv := 1
	for i := 0; i < len(f)-1; i++ {
		switch f[i] {
		case "depth":
			r.Depth, _ = strconv.Atoi(f[i+1])
		case "multipv":
			multipv, _ = strconv.Atoi(f[i+1])
		case "lowerbound", "upperbound":
			return r, false // aspiration-window bound, not a real score
		case "score":
			if i+2 >= len(f) {
				return r, false
			}
			v, err := strconv.Atoi(f[i+2])
			if err != nil {
				return r, false
			}
			switch f[i+1] {
			case "cp":
				r.CP = v
			case "mate":
				r.Mate = v
				switch {
				case v > 0:
					r.CP = MateCP - v
				case v < 0:
					r.CP = -MateCP - v
				default:
					r.CP = -MateCP // "mate 0": side to move is mated
				}
			}
		}
	}
	for _, s := range f {
		if s == "lowerbound" || s == "upperbound" {
			return r, false
		}
	}
	return r, multipv == 1
}

// Close stops the engine.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.send("quit")
	_ = e.in.Close()
	return e.cmd.Wait()
}
