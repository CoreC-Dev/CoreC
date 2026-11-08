package engine

// docs/config/global.md, global.engine.dead-letter-path:
//
//   追加式 JSONL 文件，重启时回放进内存队列（... 命令值含 64 位整数逐位保留，
//   回放不失真）
//
// "The command value's 64-bit integers are preserved bit for bit; the replay does
// not distort them."
//
// That sentence is either a guarantee about every integer above 2^53 or it is
// decoration. It fails silently when it fails: encoding/json decodes a number into
// `any` as float64, float64 has 53 bits of mantissa, and 9007199254740992 round-trips
// perfectly -- the corrupted value is simply its neighbours, an integer that looks
// like an integer, compares unequal one step over, and quietly rounds every 64-bit
// tag reading to its nearest representable neighbour. Nothing appears in the log sense.
//
// So the test walks both directions an audit entry travels:
//   in   JSON (a command-channel payload) -> WriteCommand -> DeadLetterEntry
//   out  DeadLetterEntry -> JSON (the JSONL line, and the GET /write/failed body)
// and holds the value to the same integer at every step.

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// Idioms a float64 cannot carry: 2^53+1 is the first casualty, and MaxInt64
// exercises the sign bit as well as magnitude.
var wideInt64 = []int64{
	9007199254740993, // 2^53 + 1
	-9007199254740993,
	1234567890123456789,
	9223372036854775807, // math.MaxInt64
	-9223372036854775808,
}

func TestDeadLetterPreservesInt64ThroughJSONL(t *testing.T) {
	for _, want := range wideInt64 {
		t.Run(strconv.FormatInt(want, 10), func(t *testing.T) {
			// in: a command-channel payload. The transport decodes this shape and
			// the type is on the wire, so nothing needs to be guessed.
			in := []byte(`{"driver":"plc1","tag":"big","type":"int64","value":` +
				strconv.FormatInt(want, 10) + `}`)

			var cmd core.WriteCommand
			if err := json.Unmarshal(in, &cmd); err != nil {
				t.Fatalf("decode command: %v", err)
			}
			got, ok := cmd.Value.(int64)
			if !ok {
				t.Fatalf("Value decoded as %T (%v), want int64", cmd.Value, cmd.Value)
			}
			if got != want {
				t.Fatalf("decode: Value = %d, want %d", got, want)
			}

			// out: the JSONL line a dead letter is stored as.
			line, err := json.Marshal(core.DeadLetterEntry{
				Command: cmd, Error: "paced", FailedAt: time.Now(), Attempts: 1,
			})
			if err != nil {
				t.Fatalf("encode dead letter: %v", err)
			}
			needle := `"value":` + strconv.FormatInt(want, 10)
			if !bytes.Contains(line, []byte(needle)) {
				t.Fatalf("JSONL line lost the integer: want %s in %s", needle, line)
			}

			// and back, exactly as the restart replay reads it.
			var back core.DeadLetterEntry
			if err := json.Unmarshal(line, &back); err != nil {
				t.Fatalf("replay decode: %v", err)
			}
			replayed, ok := back.Command.Value.(int64)
			if !ok {
				t.Fatalf("replayed Value = %T (%v), want int64", back.Command.Value, back.Command.Value)
			}
			if replayed != want {
				t.Fatalf("replay distorted the value: %d -> %d", want, replayed)
			}
		})
	}
}

// uint64 has the same 53-bit problem on the other side of the sign: its values run
// to ~1.8e19, far past MaxInt64, and the sentence says "64-bit integers" without
// qualifying to signed. A driver reading a uint64 counter must survive the queue too.
var wideUint64 = []uint64{
	9007199254740993,     // 2^53 + 1
	18446744073709551615, // math.MaxUint64 -- one past MaxInt64
	12345678901234567890,
}

func TestDeadLetterPreservesUint64ThroughJSONL(t *testing.T) {
	for _, want := range wideUint64 {
		t.Run(strconv.FormatUint(want, 10), func(t *testing.T) {
			in := []byte(`{"driver":"plc1","tag":"counter","type":"uint64","value":` +
				strconv.FormatUint(want, 10) + `}`)

			var cmd core.WriteCommand
			if err := json.Unmarshal(in, &cmd); err != nil {
				t.Fatalf("decode command: %v", err)
			}
			got, ok := cmd.Value.(uint64)
			if !ok {
				t.Fatalf("Value decoded as %T (%v), want uint64", cmd.Value, cmd.Value)
			}
			if got != want {
				t.Fatalf("decode: Value = %d, want %d", got, want)
			}

			line, err := json.Marshal(core.DeadLetterEntry{
				Command: cmd, Error: "paced", FailedAt: time.Now(), Attempts: 1,
			})
			if err != nil {
				t.Fatalf("encode dead letter: %v", err)
			}
			needle := `"value":` + strconv.FormatUint(want, 10)
			if !bytes.Contains(line, []byte(needle)) {
				t.Fatalf("JSONL line lost the integer: want %s in %s", needle, line)
			}

			var back core.DeadLetterEntry
			if err := json.Unmarshal(line, &back); err != nil {
				t.Fatalf("replay decode: %v", err)
			}
			replayed, ok := back.Command.Value.(uint64)
			if !ok {
				t.Fatalf("replayed Value = %T (%v), want uint64", back.Command.Value, back.Command.Value)
			}
			if replayed != want {
				t.Fatalf("replay distorted the value: %d -> %d", want, replayed)
			}
		})
	}
}

// TestDeadLetterStore64BitRoundTrip guards the persisted dead-letter
// replay: a failed 64-bit write command must survive the on-disk JSONL
// round-trip bit-exactly. The default `any` decode coerces numbers to
// float64 (9007199254740993 → 9007199254740992), which corrupted
// commands on every crash-restart replay — same class as the offline
// buffer. The WriteCommand.UnmarshalJSON type-aware decode restores
// the exact value.
func TestDeadLetterStore64BitRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deadletter.jsonl")

	st, err := newDeadLetterStore(path, 1<<20)
	if err != nil {
		t.Fatalf("newDeadLetterStore: %v", err)
	}
	entry := core.DeadLetterEntry{
		Command: core.WriteCommand{
			Driver: "plc",
			Tag:    "counter64",
			Value:  int64(9007199254740993),
			Type:   core.TypeInt64,
		},
		Error:     "device rejected",
		FailedAt:  time.Now(),
		Attempts:  0,
		Transport: "mqtt",
	}
	st.append(entry)
	st.close() // flush + fsync

	// Simulate a restart: a fresh store replays the persisted file.
	st2, err := newDeadLetterStore(path, 1<<20)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.close()
	replayed := st2.load(100)
	if len(replayed) != 1 {
		t.Fatalf("expected 1 replayed entry, got %d", len(replayed))
	}
	got, ok := replayed[0].Command.Value.(int64)
	if !ok {
		t.Fatalf("expected int64, got %T (%v)", replayed[0].Command.Value, replayed[0].Command.Value)
	}
	if got != 9007199254740993 {
		t.Fatalf("silently corrupted on DLQ replay: persisted 9007199254740993, got %d", got)
	}
	if replayed[0].Command.Type != core.TypeInt64 || replayed[0].Command.Tag != "counter64" {
		t.Fatalf("command metadata lost: %+v", replayed[0].Command)
	}
}
