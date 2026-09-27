// Command hello is a complete Aurora plugin written with the official Go SDK.
//
// Build it from this directory:
//
//	go mod init hello
//	go get github.com/Sqwid-member/Aurora-UserBot/sdk/go/aurora
//	go build -o hello .
//
// Or, inside the Aurora repository, where the SDK is available locally:
//
//	go build -o hello ./plugins/hello
//
// The binary is what the host executes; the manifest next to it is what the
// host reads. There is no build-time coupling between the two.
package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/Sqwid-member/Aurora-UserBot/sdk/go/aurora"
)

var seen atomic.Int64

func main() {
	p := aurora.New()

	p.OnStart(func(ctx context.Context) error {
		p.Infof("hello plugin starting in %s", p.Name())
		if me, err := p.GetMe(); err == nil {
			p.Infof("running as %s (id %d)", me.Name(), me.ID)
			p.Notify("Hello", fmt.Sprintf("Плагін привітався від імені %s", me.Name()), "info")
		} else {
			p.Warnf("not authorized yet: %v", err)
		}
		return nil
	})

	p.OnStop(func(ctx context.Context) error {
		p.Infof("unloaded after %d messages", seen.Load())
		return nil
	})

	p.OnEvent("core.start", func(ctx context.Context, e aurora.Event) error {
		p.Info("core started")
		return nil
	})

	p.OnEvent("session.started", func(ctx context.Context, e aurora.Event) error {
		p.Info("telegram session ready")
		return nil
	})

	p.OnEvent("message.new", func(ctx context.Context, e aurora.Event) error {
		var m aurora.Message
		if err := e.Unmarshal(&m); err != nil {
			return err
		}
		if m.Out || m.Text == "" {
			return nil
		}
		seen.Add(1)
		p.Debugf("message from %s: %s", m.PeerTitle, truncate(m.Text, 80))
		return nil
	})

	p.OnCommand("hello", func(ctx context.Context, c aurora.Command) (string, error) {
		who := "світе"
		if args := c.Split(); len(args) > 0 {
			who = args[0]
		}
		return fmt.Sprintf("Привіт, %s! 👋 (повідомлень переглянуто: %d)", who, seen.Load()), nil
	})

	if err := p.Run(os.Args[1:]); err != nil {
		p.Errorf("fatal: %v", err)
		os.Exit(1)
	}
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
