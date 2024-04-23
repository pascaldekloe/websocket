package websocket

import (
	"bytes"
	"strings"
	"testing"
	"testing/iotest"
)

func TestSmallReads(t *testing.T) {
	// final frame with binary
	const frame = "\x82\x03foo"
	const want = "foo"

	// get one byte at a time
	mock := iotest.OneByteReader(strings.NewReader(frame))

	r := NewReader(make([]byte, 4096))
	for i := 0; i < len(frame)-1; i++ {
		err := r.ReadSome(mock)
		if err != nil {
			t.Fatal("ReadSome got error:", err)
		}

		payload, err := r.NextFrame()
		if err != ErrUnderflow {
			t.Fatalf("NextFrame got %q with error %v, want ErrUnderflow",
				payload, err)
		}
	}

	err := r.ReadSome(mock)
	if err != nil {
		t.Fatal("ReadSome got error:", err)
	}
	payload, err := r.NextFrame()
	if err != nil || string(payload) != want {
		t.Fatalf("NextFrame got %q with error %v, want %q with no error",
			payload, err, want)
	}
	if code := r.Opcode(); code != Binary {
		t.Errorf("got opcode %d, want binary", code)
	}
}

func TestPingBetweenFragments(t *testing.T) {
	// Zero mask-keys keep the payload as is,
	// i.e., 0 XOR 0 is 0, and 0 XOR 1 is 1.

	var buf bytes.Buffer
	// non-final frame with text
	buf.WriteString("\x01\x86\x00\x00\x00\x00Hello ")
	// ping frame with a “application data”
	buf.WriteString("\x89\x84\x00\x00\x00\x00🔔")
	// final continuation frame (of text)
	buf.WriteString("\x80\x86\x00\x00\x00\x00World!")

	r := NewReader(make([]byte, 512))
	err := r.ReadSome(&buf)
	if err != nil {
		t.Fatal("ReadSome produced an error (not from the Reader):", err)
	}

	payload, err := r.NextFrame()
	if err != nil {
		t.Error("1st frame got error:", err)
	} else if want := "Hello "; string(payload) != want {
		t.Errorf("1st frame got %q, want %q", payload, want)
	}
	if code := r.Opcode(); code != Text {
		t.Errorf("1st frame got opcode %d, want text", code)
	}
	if r.IsFinal() {
		t.Error("1st frame got IsFinal")
	}

	payload, err = r.NextFrame()
	if err != nil {
		t.Error("2nd frame got error:", err)
	} else if want := "🔔"; string(payload) != want {
		t.Errorf("1st frame got %q, want %q", payload, want)
	}
	if code := r.Opcode(); code != Ping {
		t.Errorf("2nd frame got opcode %d, want ping", code)
	}
	if !r.IsFinal() {
		t.Error("2nd frame got not IsFinal")
	}

	payload, err = r.NextFrame()
	if err != nil {
		t.Error("3rd frame got error:", err)
	} else if want := "World!"; string(payload) != want {
		t.Errorf("3rt frame got %q, want %q", payload, want)
	}
	if code := r.Opcode(); code != Continuation {
		t.Errorf("3rd frame got opcode %d, want continuation", code)
	}
	if !r.IsFinal() {
		t.Error("3rd frame got not IsFinal")
	}
}
