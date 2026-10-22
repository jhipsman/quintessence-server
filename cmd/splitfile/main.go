// splitfile cuts a file into pieces small enough to upload, and prints the
// sha256 of the whole file so the joined copy can be checked.
//
//	splitfile.exe [-size 25] [file]
//
// Started with no file (a double-click), it splits NewWorld.zip from its own
// folder. The pieces are <file>.part1, <file>.part2, ... next to the file.
package main

import (
	"bufio"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	sizeMB := flag.Int("size", 25, "piece size in MB")
	flag.Parse()
	path := flag.Arg(0)
	if path == "" {
		path = "NewWorld.zip"
		if exe, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(exe), "NewWorld.zip")
		}
	}
	if err := split(path, int64(*sizeMB)<<20); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "Put this tool in the same folder as NewWorld.zip, or drag the file onto it.")
		}
	}
	fmt.Fprintln(os.Stderr, "\nPress Enter to close.")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func split(path string, size int64) error {
	if size <= 0 {
		return fmt.Errorf("bad piece size")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	fmt.Printf("Splitting %s (%.1f MB) into %d MB pieces...\n", path, float64(st.Size())/(1<<20), size>>20)

	whole := sha256.New()
	for n := 1; ; n++ {
		part := fmt.Sprintf("%s.part%d", path, n)
		out, err := os.Create(part)
		if err != nil {
			return err
		}
		written, err := io.CopyN(io.MultiWriter(out, whole), f, size)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if written == 0 {
			os.Remove(part)
			break
		}
		fmt.Printf("  %s  (%.1f MB)\n", filepath.Base(part), float64(written)/(1<<20))
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	fmt.Printf("\nDone. Upload every .part file. Whole file sha256:\n%x\n", whole.Sum(nil))
	return nil
}
