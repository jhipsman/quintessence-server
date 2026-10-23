package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// The stubbed login gateway: real answers for the calls of the login flow,
// written from the message layouts recovered from the client's own
// serializers (docs/PROTOCOL.md). Field numbers are the client's; names are
// the client's where it kept them, otherwise they come from how its code uses
// the field.

// The client's build. A world is only offered when its version contains the
// build's changelist ("6004151"), unless javelin.select-world-allow-version-mismatch is set.
const clientVersion = "1.400.6031.6004151"

// World is the one world this server offers.
type World struct {
	ID, Name      string
	MaxCharacters uint32
}

var defaultWorld = World{ID: "quintessence-1", Name: "Quintessence", MaxCharacters: 4}

// Character is a character as the gateway stores it.
type Character struct {
	ID      string `json:"id"`
	Persona string `json:"persona"`
	WorldID string `json:"worldId"`
	Name    string `json:"name"`
	// Published is CharacterCreationParameters field 2 (PublishedCharacterMetadata,
	// the appearance) exactly as the client sent it; it goes back to the client
	// as CharacterMetadata field 5.
	Published []byte `json:"published,omitempty"`
	Created   string `json:"created"`
}

// Gateway answers StubbedGatewayService calls and keeps the characters in a
// JSON file.
type Gateway struct {
	world World
	path  string

	mu      sync.Mutex
	chars   []Character
	tickets map[string]loginTicket // login queue tickets, by id
}

// loginTicket is what QueueLogin hands out; RefreshQueuedLogin, which only
// carries the ticket id, gets the same token back.
type loginTicket struct {
	persona, charID, worldID, signature string
}

func NewGateway(path string) (*Gateway, error) {
	g := &Gateway{world: defaultWorld, path: path, tickets: map[string]loginTicket{}}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &g.chars); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	return g, nil
}

func (g *Gateway) save() error {
	raw, err := json.MarshalIndent(g.chars, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0o755); err != nil {
		return err
	}
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, g.path)
}

// Handle answers one call. ok is false for methods it doesn't know; endpoint
// is the address the client used to reach us (the world server's address in
// the login token, for now this same server).
func (g *Gateway) Handle(method string, msgs [][]byte, endpoint string) (reply []byte, note string, ok bool) {
	var req []byte
	if len(msgs) > 0 {
		req = msgs[len(msgs)-1]
	}
	name := method[strings.LastIndex(method, "/")+1:]
	if !strings.Contains(method, "StubbedGatewayService") {
		return nil, "", false
	}
	switch name {
	case "GetLoginInfo":
		persona := personaOf(req)
		chars := g.charactersOf(persona)
		return g.loginInfo(chars), fmt.Sprintf("world %q, %d character(s) for persona %q", g.world.Name, len(chars), persona), true
	case "ValidateCharacter":
		return resultCode(0), "name accepted", true
	case "CreateCharacter":
		c, err := g.create(req)
		if err != nil {
			return resultCode(1), "create failed: " + err.Error(), true
		}
		var out []byte
		out = protowire.AppendTag(out, 2, protowire.BytesType)
		out = protowire.AppendString(out, c.ID)
		return out, fmt.Sprintf("created %q (%s) on %s", c.Name, c.ID, c.WorldID), true
	case "DeactivateCharacter":
		id := stringField(req, 1)
		g.remove(id)
		return nil, "deleted " + id, true
	case "PatchCharacter":
		return resultCode(0), "accepted", true
	case "QueueLogin":
		// QueueLoginRequest: 1 PersonaId, 2 character_id, 5 world_id (the
		// client leaves it out; the character knows its world)
		t := loginTicket{persona: personaOf(req), charID: stringField(req, 2), worldID: stringField(req, 5), signature: randomID()}
		if t.worldID == "" {
			t.worldID = g.worldOf(t.charID)
		}
		id := "ticket-" + randomID()
		g.mu.Lock()
		g.tickets[id] = t
		g.mu.Unlock()
		return queueLoginResult(id, t, endpoint), fmt.Sprintf("let in at once: ticket %s, world server %s", id, endpoint), true
	case "RefreshQueuedLogin":
		id := stringField(req, 1) // RefreshQueuedLoginRequest: 1 ticket_id
		g.mu.Lock()
		t, known := g.tickets[id]
		g.mu.Unlock()
		if !known {
			return resultCode(1), "unknown ticket " + id, true
		}
		return queueLoginResult(id, t, endpoint), "same ticket, same token: " + id, true
	}
	return nil, "", false
}

// loginInfo is GetLoginInfoResult: 1 error code (0 = none), 2 worlds,
// 3 characters, 4 character names, 5 and 6 character limits, 7 pending world merges.
func (g *Gateway) loginInfo(chars []Character) []byte {
	var out []byte
	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendBytes(out, g.worldMetadata())
	for _, c := range chars {
		out = protowire.AppendTag(out, 3, protowire.BytesType)
		out = protowire.AppendBytes(out, characterMetadata(c))
	}
	// 5 and 6 are the account's character limits (the client passes them to
	// its LoginInfoList next to the lists; most likely MaxAccountCharacters
	// and MaxChannelCharacters). Left at 0, the menu offers no character slot.
	for _, f := range []protowire.Number{5, 6} {
		out = protowire.AppendTag(out, f, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(g.world.MaxCharacters))
	}
	return out
}

// worldMetadata is WorldPublicMetadata: 1 id, 2 name, 3 status, 4
// publicStatusCode, 5 version, 6 publicName, 7 worldType ("FTUE" or
// "OpenWorld"), 8 maxAccountCharacters, 9 worldSet, 10 WorldMetrics,
// 11 transferToRegion, 12 isFull, 13 isRecommended.
func (g *Gateway) worldMetadata() []byte {
	var w []byte
	w = appendString(w, 1, g.world.ID)
	w = appendString(w, 2, g.world.Name)
	w = appendString(w, 3, "ACTIVE")
	w = appendString(w, 5, clientVersion)
	w = appendString(w, 6, g.world.Name)
	w = appendString(w, 7, "OpenWorld")
	w = protowire.AppendTag(w, 8, protowire.VarintType)
	w = protowire.AppendVarint(w, uint64(g.world.MaxCharacters))
	// WorldMetrics: 1 worldAgeDays, 2 queueSize, 3 queueWaitTimeSec, 4 worldPopulationStatus
	var m []byte
	m = protowire.AppendTag(m, 1, protowire.VarintType)
	m = protowire.AppendVarint(m, 1)
	m = appendString(m, 4, "LOW")
	w = protowire.AppendTag(w, 10, protowire.BytesType)
	w = protowire.AppendBytes(w, m)
	w = protowire.AppendTag(w, 13, protowire.VarintType)
	w = protowire.AppendVarint(w, 1)
	return w
}

// characterMetadata is CharacterMetadata: 1 character_id, 2 persona_id,
// 3 world_id, 4 name, 5 PublishedCharacterMetadata, 14 status ("ACTIVE"
// for a playable character), 21 modified_date.
func characterMetadata(c Character) []byte {
	var b []byte
	b = appendString(b, 1, c.ID)
	b = appendString(b, 2, c.Persona)
	b = appendString(b, 3, c.WorldID)
	b = appendString(b, 4, c.Name)
	if len(c.Published) > 0 {
		b = protowire.AppendTag(b, 5, protowire.BytesType)
		b = protowire.AppendBytes(b, c.Published)
	}
	b = appendString(b, 14, "ACTIVE")
	b = appendString(b, 21, c.Created)
	return b
}

// queueLoginResult is QueueLoginResult: 1 error code, 2 ticket_id,
// 3 QueueStats (its first number non-zero = still queued, no token), 4
// LoginToken. The token: 1 rep_endpoint, 2 world_id, 3 character_id,
// 4 persona_id, 8 signature, 10 ticket_id.
//
// The client only acts on a token whose signature differs from the last one
// it acted on (it starts from an empty one), so the signature must be set:
// with none, every reply looks like a repeat and the client just polls
// RefreshQueuedLogin again, every frame.
func queueLoginResult(ticket string, t loginTicket, endpoint string) []byte {
	var tok []byte
	tok = appendString(tok, 1, endpoint)
	tok = appendString(tok, 2, t.worldID)
	tok = appendString(tok, 3, t.charID)
	tok = appendString(tok, 4, t.persona)
	tok = appendString(tok, 8, t.signature)
	tok = appendString(tok, 10, ticket)
	var out []byte
	out = appendString(out, 2, ticket)
	out = protowire.AppendTag(out, 4, protowire.BytesType)
	out = protowire.AppendBytes(out, tok)
	return out
}

// worldOf is the world of a character, or this server's world.
func (g *Gateway) worldOf(charID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.chars {
		if c.ID == charID && c.WorldID != "" {
			return c.WorldID
		}
	}
	return g.world.ID
}

func (g *Gateway) charactersOf(persona string) []Character {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Character
	for _, c := range g.chars {
		if c.Persona == persona {
			out = append(out, c)
		}
	}
	return out
}

// create stores the character of a CreateCharacterRequest: 1 PersonaId,
// 2 CharacterCreationParameters (1 name, 2 PublishedCharacterMetadata), 3 world_id.
func (g *Gateway) create(req []byte) (Character, error) {
	params := bytesField(req, 2)
	c := Character{
		ID:        randomID(),
		Persona:   personaOf(req),
		WorldID:   stringField(req, 3),
		Name:      stringField(params, 1),
		Published: bytesField(params, 2),
		Created:   time.Now().UTC().Format(time.RFC3339),
	}
	if c.Name == "" {
		return c, fmt.Errorf("no name")
	}
	if c.WorldID == "" {
		c.WorldID = g.world.ID
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.chars = append(g.chars, c)
	return c, g.save()
}

func (g *Gateway) remove(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	kept := g.chars[:0]
	for _, c := range g.chars {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	g.chars = kept
	_ = g.save()
}

// ---- wire helpers ----------------------------------------------------------

func appendString(b []byte, n protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendString(b, s)
}

// resultCode is a reply whose field 1 is an error code (0 = success, the
// default, so an empty message).
func resultCode(code int32) []byte {
	if code == 0 {
		return []byte{}
	}
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	return protowire.AppendVarint(b, uint64(int64(code)))
}

// bytesField returns the last value of a length-delimited field.
func bytesField(msg []byte, num protowire.Number) []byte {
	var out []byte
	for len(msg) > 0 {
		n, typ, l := protowire.ConsumeTag(msg)
		if l < 0 {
			return out
		}
		msg = msg[l:]
		vl := protowire.ConsumeFieldValue(n, typ, msg)
		if vl < 0 {
			return out
		}
		if n == num && typ == protowire.BytesType {
			v, _ := protowire.ConsumeBytes(msg)
			out = v
		}
		msg = msg[vl:]
	}
	return out
}

func stringField(msg []byte, num protowire.Number) string { return string(bytesField(msg, num)) }

// personaOf reads the PersonaId (field 1, whose field 1 is the id) that the
// login calls start with.
func personaOf(req []byte) string { return stringField(bytesField(req, 1), 1) }

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
