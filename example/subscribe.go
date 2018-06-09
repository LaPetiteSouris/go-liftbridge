package main

import (
	"fmt"

	"golang.org/x/net/context"

	"github.com/tylertreat/go-liftbridge"
	"github.com/tylertreat/go-liftbridge/proto"
)

func main() {
	addr := "localhost:9292"
	client, err := liftbridge.Connect(addr)
	if err != nil {
		panic(err)
	}
	defer client.Close()
	ctx := context.Background()
	//ctx, cancel := context.WithCancel(ctx)
	i := 0
	if err := client.Subscribe(ctx, "foo", "foo-stream", 0, func(msg *proto.Message, err error) {
		if err != nil {
			panic(err)
		}
		fmt.Println(msg.Offset, string(msg.Value))
		i++
		if i == 5 {
			//cancel()
		}
	}); err != nil {
		panic(err)
	}

	<-ctx.Done()
}