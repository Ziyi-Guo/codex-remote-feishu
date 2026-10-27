package debuglog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRawLoggerRedactsPrivateWorkMemoryAcrossTransports(t *testing.T) {
	private := "[codex-remote-work-memory:v1]\n<work-memory>PRIVATE_CASE_2847</work-memory>"
	for _, channel := range []string{"relay.ws", "codex.stdin", "codex.stdout"} {
		t.Run(channel, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.ndjson")
			logger, err := OpenRaw(path, "test", "inst", 1)
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			frame, _ := json.Marshal(map[string]any{"input": []any{
				map[string]string{"type": "text", "text": private},
				map[string]string{"type": "text", "text": "normal user request"},
			}})
			logger.Log(RawEntry{Channel: channel, Frame: frame})
			logger.Log(RawEntry{Channel: channel, Frame: []byte("malformed wire " + private)})
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "PRIVATE_CASE_2847") {
				t.Fatal("private context reached raw log")
			}
			if !strings.Contains(string(raw), "normal user request") {
				t.Fatal("unrelated input was lost")
			}
			if !strings.Contains(string(raw), "redacted:work-memory") {
				t.Fatal("missing redaction indication")
			}
		})
	}
}
