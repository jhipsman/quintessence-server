package main

import (
	"regexp"
	"sort"
	"strings"
)

// Names are the protocol names a binary keeps even when it was built with the
// protobuf lite runtime (no schemas): gRPC method paths, which the generated
// stubs hold as plain strings, and the full names of message types, which lite
// messages still report from GetTypeName().
type Names struct {
	Methods []string // "/Package.Service/Method"
	Types   []string // "Javelin.RPC.SomeMessage"
	Related []string // other strings that mention the gateway, login, worlds, hosts or the stubbed mode
}

var (
	methodRE = regexp.MustCompile(`/[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+/[A-Za-z_][A-Za-z0-9_]*`)
	typeRE   = regexp.MustCompile(`\bJavelin(?:\.[A-Za-z_][A-Za-z0-9_]*)+`)
)

const maxRelated = 30000

// relatedWords pick the strings worth reading: the login flow, worlds (their
// status, type and set), the stubbed mode and its "Local Host" world.
var relatedWords = []string{"gateway", "login", "world", "local host", "localhost", "stub", "persona", "character"}

// FindNames scans the printable ASCII strings of a binary for protocol names.
func FindNames(data []byte) Names {
	methods, types, related := map[string]bool{}, map[string]bool{}, map[string]bool{}
	start := -1
	flush := func(end int) {
		if start < 0 || end-start < 6 {
			return
		}
		s := string(data[start:end])
		// gRPC keeps each method path as a string of its own; inside longer
		// strings (log messages) only the game's own paths are kept.
		for _, m := range methodRE.FindAllString(s, -1) {
			if m == s || strings.HasPrefix(m, "/Javelin") {
				methods[m] = true
			}
		}
		for _, t := range typeRE.FindAllString(s, -1) {
			types[strings.TrimRight(t, ".")] = true
		}
		low := strings.ToLower(s)
		if len(related) < maxRelated && containsAny(low, relatedWords) && !strings.HasPrefix(s, ".?A") {
			if len(s) > 200 {
				s = s[:200] + "…"
			}
			related[s] = true
		}
	}
	for i, c := range data {
		if c >= 0x20 && c < 0x7f {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
		start = -1
	}
	flush(len(data))

	// a type name that is only the service part of a method path is not a message
	for m := range methods {
		svc := strings.SplitN(m[1:], "/", 2)[0]
		delete(types, svc)
	}
	return Names{Methods: sorted(methods), Types: sorted(types), Related: sorted(related)}
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
