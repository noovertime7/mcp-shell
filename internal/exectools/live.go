package exectools

import (
	"bytes"
	"io"
	"log"
	"sync"
	"unicode/utf8"
)

// liveWriter publishes output while a command is running, including partial lines.
// Retain only incomplete UTF-8 bytes between writes; never buffer an unbounded line.
type liveWriter struct {
	mu      sync.Mutex
	label   string
	pending []byte
}

func (w *liveWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data := append(w.pending, p...)
	w.pending = nil
	for len(data) > 0 {
		n := len(data)
		if n > 2048 {
			n = 2048
		}
		if line := bytes.IndexByte(data[:n], '\n'); line >= 0 {
			n = line + 1
		}
		// A pipe write can end halfway through a Chinese character.
		if n == len(data) && !utf8.Valid(data[:n]) {
			for back := 1; back <= 3 && back <= n; back++ {
				start := n - back
				if utf8.RuneStart(data[start]) && !utf8.FullRune(data[start:]) {
					w.pending = append(w.pending, data[start:]...)
					data = data[:start]
					n = start
					break
				}
			}
		} else if n < len(data) {
			for n > 0 && !utf8.RuneStart(data[n]) {
				n--
			}
		}
		if n == 0 {
			break
		}
		text := bytes.TrimRight(data[:n], "\r\n")
		if len(text) > 0 {
			log.Printf("[实时输出] %s | %s", w.label, decode(text))
		}
		data = data[n:]
	}
	return len(p), nil
}

func (w *liveWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		log.Printf("[实时输出] %s | %s", w.label, decode(w.pending))
		w.pending = nil
	}
}

func streamOutput(cmdOut, cmdErr io.Writer, label string) (io.Writer, io.Writer, func()) {
	out := &liveWriter{label: label + " · 标准输出"}
	err := &liveWriter{label: label + " · 标准错误"}
	return io.MultiWriter(cmdOut, out), io.MultiWriter(cmdErr, err), func() { out.flush(); err.flush() }
}
