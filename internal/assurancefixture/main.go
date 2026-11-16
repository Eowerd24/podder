// A local static fixture, imported into an isolated store without network pulls.
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "read" {
		data, err := os.ReadFile(os.Args[2])
		if err != nil {
			fmt.Println("READ-FAILED")
			return
		}
		fmt.Print(string(data))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "writeroot" {
		if err := os.WriteFile("/root-probe", []byte("test"), 0600); err != nil {
			fmt.Println("ROOTFS-WRITE-DENIED")
			return
		}
		os.Remove("/root-probe")
		fmt.Println("ROOTFS-WRITE-ALLOWED")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "env" {
		fmt.Print(os.Getenv("PODDER_TEST_SECRET"))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "fixture") }))
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
}
