// wiredump replays the connect_pair golden trace in memory, runs the Noise XX
// handshake, and prints each decoded binary node. No real network connection is
// made; all I/O comes from the local fixture files.
//
// Usage:
//
//	go run ./cmd/wiredump
//	go run ./cmd/wiredump -trace testdata/traces/connect_pair
package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/felipeleal/wa-go/internal/wire"
)

func main() {
	traceDir := flag.String("trace", "testdata/traces/connect_pair",
		"path to the trace directory (must contain frames_raw.jsonl and noise.json)")
	flag.Parse()

	if err := run(*traceDir); err != nil {
		log.Fatalf("wiredump: %v", err)
	}
}

// frameLine mirrors one entry of frames_raw.jsonl.
type frameLine struct {
	Dir string `json:"dir"`
	Hex string `json:"hex"`
}

// noiseFixture mirrors testdata/traces/connect_pair/noise.json.
type noiseFixture struct {
	EphemeralKeyPriv    string `json:"ephemeralKeyPriv"`
	EphemeralKeyPub     string `json:"ephemeralKeyPub"`
	AuthStaticKeyPriv   string `json:"authStaticKeyPriv"`
	AuthStaticKeyPub    string `json:"authStaticKeyPub"`
	ServerHelloFrameHex string `json:"serverHelloFrameHex"`
}

func run(traceDir string) error {
	// Load noise.json.
	nf, err := loadNoiseFixture(traceDir)
	if err != nil {
		return fmt.Errorf("noise.json: %w", err)
	}

	ephPriv, err := hexDecode(nf.EphemeralKeyPriv)
	if err != nil {
		return fmt.Errorf("ephemeralKeyPriv: %w", err)
	}
	ephPub, err := hexDecode(nf.EphemeralKeyPub)
	if err != nil {
		return fmt.Errorf("ephemeralKeyPub: %w", err)
	}
	staticPriv, err := hexDecode(nf.AuthStaticKeyPriv)
	if err != nil {
		return fmt.Errorf("authStaticKeyPriv: %w", err)
	}
	staticPub, err := hexDecode(nf.AuthStaticKeyPub)
	if err != nil {
		return fmt.Errorf("authStaticKeyPub: %w", err)
	}

	// Load frames_raw.jsonl.
	frames, err := loadFrames(traceDir)
	if err != nil {
		return fmt.Errorf("frames_raw.jsonl: %w", err)
	}

	// Build the in-memory read buffer:
	//   frames[1] = ServerHello (inbound, consumed by Handshake)
	//   frames[3..] = post-handshake inbound data nodes
	//
	// Layout from connect_pair:
	//   [0] out ClientHello
	//   [1] in  ServerHello
	//   [2] out ClientFinish
	//   [3] in  pair-device (encrypted node)
	//   [4] out ...

	// Collect inbound frames after (and including) the ServerHello.
	// The loopback will serve them in sequence to the Conn.
	var inboundBuf bytes.Buffer
	seenOut := 0
	for _, fl := range frames {
		if fl.Dir == "out" {
			seenOut++
			if seenOut == 1 {
				// Skip ClientHello (Conn writes it itself).
				continue
			}
			// Skip outbound frames (ClientFinish etc.).
			continue
		}
		// dir == "in": feed to the read buffer.
		raw, err := hexDecode(fl.Hex)
		if err != nil {
			return fmt.Errorf("frame hex: %w", err)
		}
		inboundBuf.Write(raw)
	}

	rw := &replayRW{r: &inboundBuf, w: io.Discard}
	conn := wire.NewConn(rw)

	fmt.Printf("wiredump: replaying %s\n", traceDir)
	fmt.Printf("  ephemeral pub: %x\n", ephPub)

	// Run handshake (clientPayload = dummy byte, same as the test suite).
	res, err := conn.Handshake(ephPriv, ephPub, staticPriv, staticPub, []byte("x"))
	if err != nil {
		return fmt.Errorf("Handshake: %w", err)
	}
	fmt.Printf("  server static: %x\n", res.ServerStaticPub)
	fmt.Printf("  write key:     %x\n", res.WriteKey)
	fmt.Printf("  read key:      %x\n", res.ReadKey)
	fmt.Println()

	// Read and print all inbound nodes.
	nodeIdx := 0
	for {
		node, err := conn.ReadNode()
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			// Stop gracefully if the replay buffer is exhausted.
			fmt.Printf("[node %d] error: %v\n", nodeIdx, err)
			break
		}
		nodeIdx++
		printNode(nodeIdx, node)
	}

	if nodeIdx == 0 {
		return fmt.Errorf("no nodes decoded — check trace files")
	}
	fmt.Printf("\nwiredump: decoded %d node(s) — done\n", nodeIdx)
	return nil
}

// printNode prints a human-readable summary of a Node.
func printNode(idx int, n wire.Node) {
	fmt.Printf("[node %d] tag=%q\n", idx, n.Tag)
	if len(n.Attrs) > 0 {
		fmt.Printf("          attrs:\n")
		for k, v := range n.Attrs {
			fmt.Printf("            %s=%q\n", k, v)
		}
	}
	printContent(n.Content, 2)
}

func printContent(c any, depth int) {
	indent := strings.Repeat("  ", depth)
	switch v := c.(type) {
	case nil:
		// no content
	case string:
		fmt.Printf("%scontent: %q\n", indent, v)
	case []byte:
		if len(v) <= 32 {
			fmt.Printf("%scontent: [%d bytes] %x\n", indent, len(v), v)
		} else {
			fmt.Printf("%scontent: [%d bytes] %x…\n", indent, len(v), v[:32])
		}
	case []wire.Node:
		fmt.Printf("%schildren: %d\n", indent, len(v))
		for i, child := range v {
			fmt.Printf("%s  [%d] tag=%q attrs=%v\n", indent, i, child.Tag, child.Attrs)
			printContent(child.Content, depth+2)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// replayRW is an io.ReadWriteCloser backed by an in-memory buffer for reads
// and a writer (usually io.Discard) for writes.
// ──────────────────────────────────────────────────────────────────────────────

type replayRW struct {
	r io.Reader
	w io.Writer
}

func (rw *replayRW) Read(p []byte) (int, error)  { return rw.r.Read(p) }
func (rw *replayRW) Write(p []byte) (int, error) { return rw.w.Write(p) }
func (rw *replayRW) Close() error                { return nil }

// ──────────────────────────────────────────────────────────────────────────────
// File loaders
// ──────────────────────────────────────────────────────────────────────────────

func loadNoiseFixture(dir string) (noiseFixture, error) {
	path := dir + "/noise.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		return noiseFixture{}, err
	}
	var nf noiseFixture
	if err := json.Unmarshal(raw, &nf); err != nil {
		return noiseFixture{}, err
	}
	return nf, nil
}

func loadFrames(dir string) ([]frameLine, error) {
	path := dir + "/frames_raw.jsonl"
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	var out []frameLine
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var fl frameLine
		if err := json.Unmarshal(line, &fl); err != nil {
			return nil, fmt.Errorf("parse line: %w", err)
		}
		out = append(out, fl)
	}
	return out, sc.Err()
}

func hexDecode(s string) ([]byte, error) {
	return hex.DecodeString(s)
}
