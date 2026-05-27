// notify-or-email demonstrates PublishOrSink: if the user's browser session
// is still subscribed, ship to the channel; otherwise fall back to email.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/sinks"
	"github.com/frankbardon/parsec/sinks/email"
)

func main() {
	reg := sinks.NewRegistry()
	mailer, err := email.New(email.Config{
		Addr:     "smtp.example.com:587",
		From:     "no-reply@example.com",
		Username: "user",
		Password: "secret",
	})
	if err != nil {
		log.Fatal(err)
	}
	reg.Register(mailer)

	p, err := parsec.New(parsec.Options{Sinks: reg})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = p.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	const name = "private:webapp.user.42.downloads"
	creds, err := p.CreatePrivate("user-42", name, 30*time.Minute, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("private channel %s\n", creds.Name)
	fmt.Printf("  access  (exp %s): %s\n", creds.AccessExpires.Format(time.RFC3339), creds.AccessToken)
	fmt.Printf("  refresh (exp %s): %s\n", creds.RefreshExpires.Format(time.RFC3339), creds.RefreshToken)

	usedSink, err := p.PublishOrSink(ctx, name,
		[]byte(`{"status":"ready","url":"https://..."}`),
		"email",
		email.Recipient{Address: "user@example.com"},
		parsec.Message{Subject: "Your download is ready", Body: "https://..."},
	)
	if err != nil {
		log.Fatal(err)
	}
	if usedSink {
		fmt.Println("fell back to email")
	} else {
		fmt.Println("delivered to live subscriber")
	}

	// Demonstrate refresh: simulate access token expiry by minting a new
	// one from the refresh.
	refreshed, err := p.RefreshAccess(creds.RefreshToken)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("refreshed access (exp %s): %s\n",
		refreshed.AccessExpires.Format(time.RFC3339), refreshed.AccessToken)
}
