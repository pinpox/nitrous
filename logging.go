package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

// escapeContent escapes newlines and backslashes for single-line log storage.
// Backslash is escaped first to avoid double-escaping.
func escapeContent(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// unescapeContent reverses escapeContent.
func unescapeContent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '\\' {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i += 2
				continue
			case '\\':
				b.WriteByte('\\')
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// logFilePath returns the log file path for a given room.
// roomType is "channel", "group", or "dm". roomKey is the room identifier.
func logFilePath(logDir, roomType, roomKey string) string {
	// Sanitize roomKey for filesystem safety (replace path separators and special chars).
	safe := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		"\t", "_",
		":", "_",
		" ", "_",
	).Replace(roomKey)
	return filepath.Join(logDir, roomType+"_"+safe+".log")
}

// ensureLogDir creates the log directory if it doesn't exist.
func ensureLogDir(logDir string) error {
	return os.MkdirAll(logDir, 0755)
}

// appendLogEntry appends a single message to the room's log file.
func appendLogEntry(logDir, roomType, roomKey string, msg ChatMessage, displayName string) {
	if logDir == "" {
		return
	}
	if err := ensureLogDir(logDir); err != nil {
		log.Printf("logging: failed to create log dir: %v", err)
		return
	}

	path := logFilePath(logDir, roomType, roomKey)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("logging: failed to open %s: %v", path, err)
		return
	}
	defer f.Close()

	ts := time.Unix(int64(msg.Timestamp), 0).UTC().Format("2006-01-02 15:04:05")

	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n", ts, msg.EventID, msg.PubKey, displayName, escapeContent(msg.Content))
	if _, err := f.WriteString(line); err != nil {
		log.Printf("logging: failed to write to %s: %v", path, err)
	}
}

// loadLogHistory loads the last maxMessages entries from a room's log file
// using backward seeking for efficiency on large files.
func loadLogHistory(logDir, roomType, roomKey string, maxMessages int) ([]ChatMessage, error) {
	if logDir == "" {
		return nil, nil
	}

	path := logFilePath(logDir, roomType, roomKey)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("logging: open %s: %w", path, err)
	}
	defer f.Close()

	lines, err := readLastNLines(f, maxMessages)
	if err != nil {
		return nil, fmt.Errorf("logging: read %s: %w", path, err)
	}

	msgs := make([]ChatMessage, 0, len(lines))
	for _, line := range lines {
		msg, err := parseLogLine(line)
		if err != nil {
			log.Printf("logging: skipping malformed line in %s: %v", path, err)
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// readLastNLines reads the last n lines from a file by seeking backward.
func readLastNLines(f *os.File, n int) ([]string, error) {
	const chunkSize = 8192

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()
	if size == 0 {
		return nil, nil
	}

	var buf []byte
	offset := size
	linesFound := 0

	for offset > 0 && linesFound <= n {
		readSize := int64(chunkSize)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, offset); err != nil && err != io.EOF {
			return nil, err
		}

		buf = append(chunk, buf...)

		// Count newlines in the chunk.
		for _, b := range chunk {
			if b == '\n' {
				linesFound++
			}
		}
	}

	// Split into lines and take the last n non-empty lines.
	scanner := bufio.NewScanner(strings.NewReader(string(buf)))
	var allLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			allLines = append(allLines, line)
		}
	}

	if len(allLines) > n {
		allLines = allLines[len(allLines)-n:]
	}
	return allLines, nil
}

// parseLogLine parses a single tab-separated log line into a ChatMessage.
func parseLogLine(line string) (ChatMessage, error) {
	parts := strings.SplitN(line, "\t", 5)
	if len(parts) < 5 {
		return ChatMessage{}, fmt.Errorf("expected 5 tab-separated fields, got %d", len(parts))
	}

	ts, err := time.Parse("2006-01-02 15:04:05", parts[0])
	if err != nil {
		return ChatMessage{}, fmt.Errorf("invalid timestamp %q: %w", parts[0], err)
	}

	return ChatMessage{
		Timestamp: nostr.Timestamp(ts.Unix()),
		EventID:   parts[1], // short 8-char ID
		PubKey:    parts[2], // short 8-char pubkey
		Author:    parts[3],
		Content:   unescapeContent(parts[4]),
	}, nil
}

// logRoomType returns the log room type string for a ChatMessage based on context.
func logRoomType(cm ChatMessage) string {
	if cm.GroupKey != "" {
		return "group"
	}
	if cm.ChannelID != "" {
		return "channel"
	}
	return "dm"
}

// logRoomKey returns the log room key for a ChatMessage.
func logRoomKey(cm ChatMessage) string {
	if cm.GroupKey != "" {
		return cm.GroupKey
	}
	if cm.ChannelID != "" {
		return cm.ChannelID
	}
	return cm.PubKey
}
