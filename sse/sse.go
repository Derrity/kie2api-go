package sse

import (
	"bufio"
	"io"
	"net/http"
	"strings"
)

// Event is one parsed Server-Sent Event line group.
type Event struct {
	Event string
	Data  string
}

// Reader parses an SSE stream from an upstream response body.
type Reader struct {
	br *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 64*1024)}
}

// Next returns the next event, or io.EOF when stream ends.
// Lines beginning with ":" (comments) are ignored.
func (r *Reader) Next() (*Event, error) {
	var ev Event
	var dataLines []string
	gotAny := false
	for {
		line, err := r.br.ReadString('\n')
		if err != nil {
			if (err == io.EOF || err == io.ErrUnexpectedEOF) && (gotAny || strings.TrimSpace(line) != "") {
				if line != "" {
					processLine(strings.TrimRight(line, "\r\n"), &ev, &dataLines)
				}
				if len(dataLines) > 0 {
					ev.Data = strings.Join(dataLines, "\n")
					return &ev, nil
				}
			}
			return nil, err
		}
		raw := strings.TrimRight(line, "\r\n")
		if raw == "" {
			if gotAny {
				ev.Data = strings.Join(dataLines, "\n")
				return &ev, nil
			}
			continue
		}
		gotAny = true
		processLine(raw, &ev, &dataLines)
	}
}

func processLine(raw string, ev *Event, dataLines *[]string) {
	if strings.HasPrefix(raw, ":") {
		return
	}
	if i := strings.Index(raw, ":"); i >= 0 {
		field := raw[:i]
		val := raw[i+1:]
		if strings.HasPrefix(val, " ") {
			val = val[1:]
		}
		switch field {
		case "event":
			ev.Event = val
		case "data":
			*dataLines = append(*dataLines, val)
		}
	}
}

// Writer streams SSE events to a client ResponseWriter, flushing after each.
type Writer struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func NewWriter(w http.ResponseWriter) *Writer {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)
	return &Writer{w: w, fl: fl}
}

// Write emits one event. event may be empty for OpenAI-style "data:"-only streams.
func (w *Writer) Write(event, data string) error {
	var b strings.Builder
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if _, err := io.WriteString(w.w, b.String()); err != nil {
		return err
	}
	if w.fl != nil {
		w.fl.Flush()
	}
	return nil
}

// Done writes the "data: [DONE]" terminator (OpenAI convention).
func (w *Writer) Done() error {
	if _, err := io.WriteString(w.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if w.fl != nil {
		w.fl.Flush()
	}
	return nil
}

// Passthrough copies an SSE response body verbatim to the client, flushing
// frequently so chunks reach the client in real time.
func Passthrough(w http.ResponseWriter, body io.Reader) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 8*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
