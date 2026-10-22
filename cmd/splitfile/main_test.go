package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestSplitJoinsBack(t *testing.T) {
	for _, n := range []int{0, 1, 999, 1000, 1001, 3500} {
		dir := t.TempDir()
		path := filepath.Join(dir, "NewWorld.zip")
		data := make([]byte, n)
		rand.Read(data)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := split(path, 1000); err != nil {
			t.Fatal(err)
		}
		var joined []byte
		parts := 0
		for i := 1; ; i++ {
			b, err := os.ReadFile(path + ".part" + string(rune('0'+i)))
			if err != nil {
				break
			}
			parts++
			joined = append(joined, b...)
		}
		if !bytes.Equal(joined, data) {
			t.Fatalf("size %d: joined %d bytes from %d parts", n, len(joined), parts)
		}
		if want := (n + 999) / 1000; parts != want {
			t.Errorf("size %d: %d parts, want %d", n, parts, want)
		}
	}
}
