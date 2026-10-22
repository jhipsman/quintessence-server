// protoextract finds the protobuf schemas compiled into a program and writes
// them back out as .proto files.
//
// Programs built with the full C++ (or Go) protobuf runtime carry every .proto
// file they use as a serialized FileDescriptorProto, the same bytes protoc
// produced. This tool scans a file for them: each starts with its own file
// name ("....proto") as field 1. It only reads the file it is given.
//
//	protoextract.exe "F:\SteamLibrary\steamapps\common\New World\Bin64\NewWorld.exe"
//
// Started with no file (a double-click), it takes NewWorld.exe from the New
// World folder saved in the Quintessence launcher's settings, or asks for it.
// It writes, next to the tool (or in -out):
//
//	protos\<path>.proto  one file per schema found
//	protos\ALL.txt       all of them in one text file, to share
//	protos\descriptors.pb the raw FileDescriptorSet
//	protos\NAMES.txt     gRPC method paths and message type names, which
//	                     survive even when the schemas don't (lite runtime)
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func main() {
	out := flag.String("out", "", "folder for the .proto files (default: protos, next to this tool)")
	all := flag.Bool("all", false, "also write google/protobuf and other library schemas")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: protoextract [-out protos] [-all] <program.exe>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 1 {
		flag.Usage()
		pause()
		os.Exit(2)
	}
	path := flag.Arg(0)
	if path == "" {
		path = askForGame()
	}
	if *out == "" {
		*out = "protos"
		if exe, err := os.Executable(); err == nil {
			*out = filepath.Join(filepath.Dir(exe), "protos")
		}
	}
	if err := run(path, *out, *all); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		pause()
		os.Exit(1)
	}
	pause()
}

// askForGame finds NewWorld.exe through the launcher's settings, or asks for
// it: dragging a file into a console window types its path.
func askForGame() string {
	if dir := launcherGameDir(); dir != "" {
		for _, p := range []string{filepath.Join(dir, "Bin64", "NewWorld.exe"), filepath.Join(dir, "NewWorld.exe")} {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				fmt.Printf("Using the New World folder from the launcher's settings.\n")
				return p
			}
		}
	}
	fmt.Println("Drag NewWorld.exe (in your New World folder, under Bin64) into this window, then press Enter:")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.Trim(strings.TrimSpace(line), `"'&`+" ")
}

// launcherGameDir is the New World folder saved by the Quintessence launcher.
func launcherGameDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(base, "QuintessenceLauncher", "config.json"))
	if err != nil {
		return ""
	}
	var c struct {
		GameDir string `json:"gameDir"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	return c.GameDir
}

// pause keeps the window open when the tool was started by dropping a file on it.
func pause() {
	fmt.Fprintln(os.Stderr, "\nPress Enter to close.")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func run(path, outDir string, all bool) error {
	fmt.Printf("Reading %s ...\n", path)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := writeNames(FindNames(data), filepath.Join(outDir, "NAMES.txt")); err != nil {
		return err
	}

	files := Extract(data)
	if !all {
		kept := files[:0]
		for _, f := range files {
			if !isLibrary(f.GetName()) {
				kept = append(kept, f)
			}
		}
		files = kept
	}
	if len(files) == 0 {
		fmt.Println("\nNo .proto schemas found (the program may use the protobuf lite runtime, which carries none).")
		abs, _ := filepath.Abs(filepath.Join(outDir, "NAMES.txt"))
		fmt.Printf("Wrote %s\n", abs)
		return nil
	}

	var allText strings.Builder
	set := &descriptorpb.FileDescriptorSet{}
	for _, f := range files {
		text := PrintProto(f)
		dst := filepath.Join(outDir, filepath.FromSlash(safePath(f.GetName())))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(&allText, "// ===== %s =====\n%s\n", f.GetName(), text)
		set.File = append(set.File, f)
	}
	if err := os.WriteFile(filepath.Join(outDir, "ALL.txt"), []byte(allText.String()), 0o644); err != nil {
		return err
	}
	raw, err := proto.Marshal(set)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "descriptors.pb"), raw, 0o644); err != nil {
		return err
	}

	fmt.Printf("\nFound %d schema files. Services:\n", len(files))
	var lines []string
	for _, f := range files {
		for _, s := range f.GetService() {
			for _, m := range s.GetMethod() {
				lines = append(lines, fmt.Sprintf("  %s.%s/%s", f.GetPackage(), s.GetName(), m.GetName()))
			}
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
	abs, _ := filepath.Abs(filepath.Join(outDir, "ALL.txt"))
	names, _ := filepath.Abs(filepath.Join(outDir, "NAMES.txt"))
	fmt.Printf("\nWrote %s\nand %s\n", abs, names)
	return nil
}

// writeNames writes the protocol names found in the binary and prints the
// method count.
func writeNames(n Names, path string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# gRPC methods (%d)\n", len(n.Methods))
	for _, m := range n.Methods {
		b.WriteString(m + "\n")
	}
	fmt.Fprintf(&b, "\n# Javelin type names (%d)\n", len(n.Types))
	for _, t := range n.Types {
		b.WriteString(t + "\n")
	}
	fmt.Fprintf(&b, "\n# Other strings mentioning the gateway, login, worlds, hosts, stubs, personas or characters (%d)\n", len(n.Related))
	for _, r := range n.Related {
		b.WriteString(r + "\n")
	}
	fmt.Fprintf(&b, "\n# Embedded JSON documents, in full (%d)\n", len(n.JSON))
	for _, j := range n.JSON {
		b.WriteString(j + "\n\n")
	}
	fmt.Printf("Found %d gRPC methods, %d Javelin type names and %d JSON documents.\n", len(n.Methods), len(n.Types), len(n.JSON))
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// isLibrary is a schema that comes with protobuf or gRPC, not the game.
func isLibrary(name string) bool {
	return strings.HasPrefix(name, "google/protobuf/") || strings.HasPrefix(name, "grpc/")
}

// safePath keeps a schema's path inside the output folder.
func safePath(name string) string {
	var parts []string
	for _, p := range strings.Split(strings.ReplaceAll(name, `\`, "/"), "/") {
		if p != "" && p != "." && p != ".." && !strings.Contains(p, ":") {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "unnamed.proto"
	}
	return strings.Join(parts, "/")
}
