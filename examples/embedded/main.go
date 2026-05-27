// Embedded shows how a Go service uses parsec as a library. No CLI, no
// twirp — just the library facade and one public channel.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/frankbardon/parsec"
)

func main() {
	p, err := parsec.New(parsec.Options{})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = p.Run(ctx) }()
	time.Sleep(100 * time.Millisecond) // wait for broker

	ch, err := p.OpenPublic("public:demo.system.status", 0)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("opened %s (state=%s)\n", ch.Name, ch.State)

	res, err := p.Publish(ctx, ch.Name.String(), []byte(`{"ok":true}`))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("published at offset %d (epoch %s)\n", res.Offset, res.Epoch)
}
