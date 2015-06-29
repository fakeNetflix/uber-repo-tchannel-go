package main

// Copyright (c) 2015 Uber Technologies, Inc.

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/net/context"

	"github.com/uber/tchannel/golang"
	"github.com/uber/tchannel/golang/examples/keyvalue/gen-go/keyvalue"
	"github.com/uber/tchannel/golang/hyperbahn"
	"github.com/uber/tchannel/golang/thrift"
)

func printHelp() {
	fmt.Printf("Usage:\n get [key]\n set [key] [value]\n")
}

func main() {
	// Create a TChannel.
	ch, err := tchannel.NewChannel("keyvalue-client", nil)
	if err != nil {
		log.Fatalf("Failed to create tchannel: %v", err)
	}

	// Set up Hyperbahn client.
	config := hyperbahn.Configuration{InitialNodes: os.Args[1:]}
	if len(config.InitialNodes) == 0 {
		log.Fatalf("No Autobahn nodes to connect to given")
	}
	hyperbahn.NewClient(ch, config, nil)

	// Read commands from the command line and execute them.
	scanner := bufio.NewScanner(os.Stdin)
	printHelp()
	fmt.Printf("> ")
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), " ")
		if parts[0] == "" {
			continue
		}
		switch parts[0] {
		case "help":
			printHelp()
		case "get":
			if len(parts) < 2 {
				printHelp()
				break
			}
			get(ch, parts[1])
		case "set":
			if len(parts) < 3 {
				printHelp()
				break
			}
			set(ch, parts[1], parts[2])
		default:
			log.Printf("Unsupported command %q\n", parts[0])
		}
		fmt.Print("> ")
	}
	scanner.Text()
}

func getClient(ch *tchannel.Channel) *keyvalue.KeyValueClient {
	ctx, _ := context.WithTimeout(context.Background(), time.Second*10)

	ctx = tchannel.NewRootContext(ctx)
	protocol := thrift.NewTChanOutbound(ch, thrift.TChanOutboundOptions{
		Context:          ctx,
		HyperbahnService: "keyvalue",
		ThriftService:    "KeyValue",
	})
	client := keyvalue.NewKeyValueClientProtocol(nil, protocol, protocol)
	return client
}

func get(ch *tchannel.Channel, key string) {
	client := getClient(ch)
	val, err := client.Get(key)
	if err != nil {
		log.Printf("Get %v got err: %v", key, err)
		return
	}

	log.Printf("Get %v: %v", key, val)
}

func set(ch *tchannel.Channel, key, value string) {
	client := getClient(ch)
	if err := client.Set(key, value); err != nil {
		log.Printf("Set %v:%v got err: %v", key, value, err)
	}
	log.Printf("Set %v:%v succeeded", key, value)
}