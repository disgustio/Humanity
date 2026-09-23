package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// ---------- CONFIG ----------
var (
	ListenAddr = "0.0.0.0:19132"
	RealmCode  = ""                         // set with .humanity realm <code>
	ServerAddr = "bedrock.example.net:19132" // set with .humanity server <ip:port>
)

// ---------- FEATURES ----------
type Features struct {
	mu sync.Mutex

	ShowKeystrokes bool
	ShowCPS        bool
	ShowPing       bool
	ShowCombo      bool
	ShowTotems     bool
	ShowSession    bool
	AutoGG         bool

	CPSLimit     int
	CPS          int
	Combo        int
	MaxCombo     int
	Totems       int
	Hits         int
	Swings       int
	Ping         time.Duration
	SessionStart time.Time

	Keys map[string]bool

	lastAttack  time.Time
	lastHit     time.Time
	attackTimes []time.Time
}

var F = &Features{
	ShowKeystrokes: true,
	ShowCPS:        true,
	ShowPing:       true,
	ShowCombo:      true,
	ShowTotems:     true,
	ShowSession:    true,
	AutoGG:         true,
	CPSLimit:       0,
	Keys:           map[string]bool{},
	SessionStart:   time.Now(),
}

func main() {
	log.Println("Humanity proxy starting...")

	listener, err := minecraft.ListenConfig{
		StatusProvider: minecraft.NewStatusProvider("Humanity", "Proxy"),
	}.Listen("raknet", ListenAddr)
	if err != nil {
		log.Fatalf("listen error: %v", err)
	}
	defer listener.Close()
	log.Printf("Listening on %s", ListenAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("accept error: %v", err)
			continue
		}
		go handleClient(conn.(*minecraft.Conn))
	}
}

func handleClient(client *minecraft.Conn) {
	log.Printf("PS4 connected: %s", client.IdentityData().DisplayName)

	dialer := minecraft.Dialer{
		IdentityData: client.IdentityData(),
		ClientData:   client.ClientData(),
	}

	var server *minecraft.Conn
	var err error

	if RealmCode != "" {
		server, err = dialer.DialRealm(RealmCode)
	} else {
		server, err = dialer.Dial("raknet", ServerAddr)
	}

	if err != nil {
		log.Printf("dial error: %v", err)
		client.Close()
		return
	}
	defer server.Close()

	if err := client.StartGame(server.GameData()); err != nil {
		log.Printf("client start error: %v", err)
		return
	}
	if err := server.StartGame(client.GameData()); err != nil {
		log.Printf("server start error: %v", err)
		return
	}

	F.mu.Lock()
	F.SessionStart = time.Now()
	F.Combo = 0
	F.Totems = 0
	F.Hits = 0
	F.Swings = 0
	F.mu.Unlock()

	go pingLoop(client)
	go hudLoop(client)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for {
			pk, err := client.ReadPacket()
			if err != nil {
				return
			}
			if handleClientPacket(client, server, pk) {
				if err := server.WritePacket(pk); err != nil {
					return
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		for {
			pk, err := server.ReadPacket()
			if err != nil {
				return
			}
			if err := client.WritePacket(pk); err != nil {
				return
			}
		}
	}()

	wg.Wait()
	log.Println("Session ended")
}

func handleClientPacket(client, server *minecraft.Conn, pk packet.Packet) bool {
	F.mu.Lock()
	defer F.mu.Unlock()

	switch p := pk.(type) {

	case *packet.Interact:
		if p.ActionType == packet.InteractActionAttack ||
			p.ActionType == packet.InteractActionMouseOverEntity {
			now := time.Now()
			F.Swings++
			F.attackTimes = append(F.attackTimes, now)

			if F.CPSLimit > 0 {
				minDelay := time.Duration(1000/F.CPSLimit) * time.Millisecond
				if now.Sub(F.lastAttack) < minDelay {
					return false
				}
			}
			F.lastAttack = now

			if now.Sub(F.lastHit) < 3*time.Second {
				F.Combo++
				if F.Combo > F.MaxCombo {
					F.MaxCombo = F.Combo
				}
			} else {
				F.Combo = 1
			}
			F.lastHit = now
			F.Hits++
		}

	case *packet.PlayerAuthInput:
		flags := p.InputFlags
		F.Keys["W"] = flags&packet.InputFlagUp != 0
		F.Keys["S"] = flags&packet.InputFlagDown != 0
		F.Keys["A"] = flags&packet.InputFlagLeft != 0
		F.Keys["D"] = flags&packet.InputFlagRight != 0
		F.Keys["Space"] = flags&packet.InputFlagJumping != 0
		F.Keys["Shift"] = flags&packet.InputFlagSneaking != 0

	case *packet.Text:
		if p.TextType == packet.TextTypeChat {
			msg := strings.TrimSpace(p.Message)
			if strings.HasPrefix(msg, ".humanity") {
				handleChatCommand(client, msg)
				return false
			}
			if F.AutoGG {
				low := strings.ToLower(msg)
				name := strings.ToLower(client.IdentityData().DisplayName)
				if strings.Contains(low, "slain by "+name) ||
					strings.Contains(low, name+" killed") ||
					strings.Contains(low, "you killed") {
					sendChat(server, "GG")
				}
			}
		}
	}

	return true
}

func handleChatCommand(client *minecraft.Conn, msg string) {
	parts := strings.Fields(msg)
	if len(parts) < 2 {
		return
	}
	switch parts[1] {
	case "keys":
		if len(parts) > 2 {
			F.ShowKeystrokes = parts[2] == "on"
		}
	case "cps":
		if len(parts) > 2 {
			F.ShowCPS = parts[2] == "on"
		}
	case "ping":
		if len(parts) > 2 {
			F.ShowPing = parts[2] == "on"
		}
	case "combo":
		if len(parts) > 2 {
			F.ShowCombo = parts[2] == "on"
		}
	case "totem":
		if len(parts) > 2 {
			F.ShowTotems = parts[2] == "on"
		}
	case "session":
		if len(parts) > 2 {
			F.ShowSession = parts[2] == "on"
		}
	case "autogg":
		if len(parts) > 2 {
			F.AutoGG = parts[2] == "on"
		}
	case "cpslimit":
		if len(parts) > 2 {
			if parts[2] == "off" {
				F.CPSLimit = 0
			} else {
				fmt.Sscanf(parts[2], "%d", &F.CPSLimit)
			}
		}
	case "realm":
		if len(parts) > 2 {
			RealmCode = parts[2]
			ServerAddr = ""
			sendChat(client, "Realm set. Reconnect to apply.")
		}
	case "server":
		if len(parts) > 2 {
			ServerAddr = parts[2]
			RealmCode = ""
			sendChat(client, "Server set. Reconnect to apply.")
		}
	}
}

func pingLoop(conn *minecraft.Conn) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		start := time.Now()
		conn.WritePacket(&packet.NetworkSettings{})
		F.mu.Lock()
		F.Ping = time.Since(start)
		F.mu.Unlock()
	}
}

func hudLoop(conn *minecraft.Conn) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		F.mu.Lock()

		now := time.Now()
		cutoff := now.Add(-1 * time.Second)
		var recent []time.Time
		for _, t := range F.attackTimes {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}
		F.attackTimes = recent
		F.CPS = len(recent)

		if F.ShowKeystrokes {
			keys := ""
			if F.Keys["W"] { keys += "W " }
			if F.Keys["A"] { keys += "A " }
			if F.Keys["S"] { keys += "S " }
			if F.Keys["D"] { keys += "D " }
			if F.Keys["Space"] { keys += "[Space] " }
			if F.Keys["Shift"] { keys += "[Shift] " }
			sendActionBar(conn, "Keys: "+strings.TrimSpace(keys))
		}

		var lines []string
		if F.ShowCPS {
			lines = append(lines, fmt.Sprintf("CPS: %d", F.CPS))
		}
		if F.ShowPing {
			lines = append(lines, fmt.Sprintf("Ping: %dms", F.Ping.Milliseconds()))
		}
		if F.ShowCombo {
			lines = append(lines, fmt.Sprintf("Combo: %d", F.Combo))
		}
		if F.ShowTotems {
			lines = append(lines, fmt.Sprintf("Totems: %d", F.Totems))
		}
		if F.ShowSession {
			lines = append(lines, fmt.Sprintf("Session: %s", time.Since(F.SessionStart).Round(time.Second)))
		}
		if len(lines) > 0 {
			sendSidebar(conn, lines)
		}

		F.mu.Unlock()
	}
}

func sendActionBar(conn *minecraft.Conn, text string) {
	conn.WritePacket(&packet.SetTitle{
		ActionType:     packet.TitleActionSetActionBar,
		Text:           text,
		FadeInDuration: 0,
		RemainDuration: 30,
		FadeOutDuration: 0,
	})
}

func sendTopLeft(conn *minecraft.Conn, text string) {
	padded := text + strings.Repeat("\n", 20) + strings.Repeat(" ", 120)
	conn.WritePacket(&packet.SetTitle{
		ActionType:     packet.TitleActionSetTitle,
		Text:           padded,
		FadeInDuration: 0,
		RemainDuration: 20,
		FadeOutDuration: 0,
	})
}

func sendSidebar(conn *minecraft.Conn, lines []string) {
	conn.WritePacket(&packet.SetDisplayObjective{
		DisplaySlot:   packet.ScoreboardDisplaySlotSidebar,
		ObjectiveName: "humanity",
		DisplayName:   "Humanity",
		CriteriaName:  "dummy",
		SortOrder:     0,
	})
	for i, line := range lines {
		conn.WritePacket(&packet.SetScore{
			ActionType: packet.ScoreboardActionModify,
			Entries: []protocol.ScoreEntry{
				{
					ScoreboardID:   int64(i),
					ObjectiveName:  "humanity",
					Score:          int32(len(lines) - i),
					IdentityType:   protocol.ScoreIdentityTypeFakePlayer,
					FakePlayerName: line,
				},
			},
		})
	}
}

func sendToast(conn *minecraft.Conn, title, content string) {
	conn.WritePacket(&packet.ToastRequest{
		Title:   title,
		Content: content,
	})
}

func sendChat(conn *minecraft.Conn, msg string) {
	conn.WritePacket(&packet.Text{
		TextType: packet.TextTypeChat,
		Message:  msg,
	})
}
