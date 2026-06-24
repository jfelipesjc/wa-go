// Command wa-features relogs in with saved creds and exercises the lib features
// that aren't surfaced in the wa/ facade or wa-evolution yet — profile, status
// (stories), newsletters and app-state resync — so they can be validated LIVE.
// Run with a creds DB that is ALREADY paired (e.g. from wa-paircode). Connects to
// REAL WhatsApp; do NOT run a second session on the same creds concurrently.
//
//	go run ./cmd/wa-features -db ./creds.db -recipient 5512...@s.whatsapp.net
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/felipeleal/wa-go/internal/client"
	"github.com/felipeleal/wa-go/internal/store"
)

func main() {
	db := flag.String("db", "", "path to an ALREADY-PAIRED creds DB")
	recipient := flag.String("recipient", "", "a JID to target for status/archive (e.g. 5512...@s.whatsapp.net)")
	timeout := flag.Duration("timeout", 90*time.Second, "overall timeout")
	flag.Parse()
	if *db == "" {
		fmt.Fprintln(os.Stderr, "wa-features: -db is required")
		os.Exit(2)
	}
	if err := run(*db, *recipient, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "wa-features: %v\n", err)
		os.Exit(1)
	}
}

func run(db, recipient string, timeout time.Duration) error {
	st, err := store.OpenSQLite(db)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	creds, ok, err := st.LoadCreds()
	if err != nil || !ok || !creds.Registered {
		return fmt.Errorf("creds not registered (pair first): ok=%v err=%v", ok, err)
	}
	me := creds.Me
	fmt.Printf("wa-features: db=%s me=%s recipient=%s\n", db, me, recipient)

	client.EnableDebug(os.Stderr)
	c := client.New(st)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	connErr := make(chan error, 1)
	go func() { connErr <- c.Connect(ctx) }()

	step := func(name string, fn func() error) {
		err := fn()
		if err != nil {
			fmt.Printf("  ❌ %-22s %v\n", name, err)
		} else {
			fmt.Printf("  ✅ %-22s OK\n", name)
		}
	}

	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				<-connErr
				return nil
			}
			switch e.(type) {
			case client.LoggedInEvent:
				time.Sleep(2 * time.Second)
				fmt.Println("\n=== Validando features ao vivo ===")

				// PERFIL: muda o status (recado). O usuário confirma no celular.
				want := fmt.Sprintf("wa-go status live %d", time.Now().Unix())
				step("PerfilSetStatus", func() error { return c.UpdateProfileStatus(ctx, want) })

				// NEWSLETTERS: cria um canal.
				step("NewsletterCreate", func() error {
					info, err := c.NewsletterCreate(ctx, "wa-go canal teste", "validação live")
					if err == nil && info != nil {
						fmt.Printf("       └─ canal: %s\n", info.JID)
					}
					return err
				})

				// NOVOS (3 gaps): enviar enquete + preview de convite de grupo.
				if recipient != "" {
					step("SendPoll", func() error {
						_, err := c.SendPoll(ctx, recipient, "wa-go enquete teste?", []string{"Sim", "Não", "Talvez"}, 1)
						return err
					})
				}
				step("GroupGetInviteInfo", func() error {
					info, err := c.GroupGetInviteInfo(ctx, "HmGw3DvDXF4J3Aw9wz5R2w")
					if err == nil && info != nil {
						fmt.Printf("       └─ grupo do convite: %q (%d membros)\n", info.Subject, len(info.Participants))
					}
					return err
				})

				// APP-STATE RESYNC primeiro: estabelece a versão das coleções antes
				// de qualquer mutação (archive/pin precisam da versão atual).
				step("AppStateResync", func() error {
					return c.ResyncAppState(ctx, []string{"regular_high", "regular_low", "critical_block"}, true)
				})

				// APP-STATE write: arquivar e desarquivar um chat (o mais usado).
				if recipient != "" {
					step("ArchiveChat(on/off)", func() error {
						if err := c.ArchiveChat(ctx, recipient, true); err != nil {
							return err
						}
						return c.ArchiveChat(ctx, recipient, false)
					})
					step("PinChat(on/off)", func() error {
						if err := c.PinChat(ctx, recipient, true); err != nil {
							return err
						}
						return c.PinChat(ctx, recipient, false)
					})
				}

				// STATUS/STORIES: posta um story (precisa de destinatário).
				if recipient != "" {
					step("Status/Story(post)", func() error {
						_, err := c.SendStatusText(ctx, "wa-go story de teste 📣", []string{recipient})
						return err
					})
				}

				// PERFIL leitura: status de OUTRO (padrão; self-fetch não é suportado).
				if recipient != "" {
					step("FetchStatus(destinatário)", func() error {
						got, err := c.FetchStatus(ctx, recipient)
						if err != nil {
							return err
						}
						fmt.Printf("       └─ status do destinatário: %q\n", got)
						return nil
					})
				}

				_ = me

				fmt.Println("=== fim ===")
				cancel()
			case client.DisconnectedEvent:
				// ignore; loop ends when Connect returns
			}
		case err := <-connErr:
			return err
		case <-ctx.Done():
			return nil
		}
	}
}
