//go:build !generate

package main

import (
    "fmt"
    "time"
    "os"
	"github.com/sagernet/sing-box/log"
)

func main() {
	fmt.Println("Tapir-Box v0.3")
	
	go func() {
        time.Sleep(200 * time.Second)
        fmt.Println("Exit: Timeout")
        os.Exit(1)
    }()

	if err := mainCommand.Execute(); err != nil {
		log.Fatal(err)
	}
}
