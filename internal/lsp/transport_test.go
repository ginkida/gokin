package lsp

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func TestReadFrameBoundsAndFraming(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":null}`
	valid := fmt.Sprintf("Content-Length: %d\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n%s", len(body), body)
	if msg, err := readFrame(bufio.NewReaderSize(strings.NewReader(valid), maxHeaderBytes)); err != nil || string(msg.Result) != "null" {
		t.Fatalf("valid frame=%+v %v", msg, err)
	}
	for _, raw := range []string{"logging on stdout\r\n", "Content-Length: 999999999\r\n\r\n", "Content-Length: -1\r\n\r\n", "Content-Length: 1\r\nContent-Length: 1\r\n\r\nx", "Content-Length: 1\n\nx", "Content-Length: 1\r\nContent-Type: application/json; charset=utf-16\r\n\r\nx", strings.Repeat("x", maxHeaderBytes+1)} {
		if _, err := readFrame(bufio.NewReaderSize(strings.NewReader(raw), maxHeaderBytes)); err == nil {
			t.Fatalf("accepted invalid header length=%d", len(raw))
		}
	}
}
func TestSourcePositionEncodings(t *testing.T) {
	for _, tc := range []struct {
		encoding string
		want     int
	}{{"utf-8", 5}, {"utf-16", 3}, {"utf-32", 2}} {
		position, err := sourcePosition("a😀b\r\nnext", 1, 3, tc.encoding)
		if err != nil || position.Character != tc.want {
			t.Fatalf("%s position=%+v %v", tc.encoding, position, err)
		}
	}
	if end := endPosition("a😀\r\nlast", "utf-16"); end.Line != 1 || end.Character != 4 {
		t.Fatal(end)
	}
	for _, pos := range [][2]int{{0, 1}, {1, 0}, {1, 99}, {9, 1}} {
		if _, err := sourcePosition("a", pos[0], pos[1], "utf-16"); err == nil {
			t.Fatalf("accepted %v", pos)
		}
	}
}
